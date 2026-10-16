package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mostlygeek/llama-swap/internal/auth"
	"github.com/mostlygeek/llama-swap/internal/config"
)

const (
	adminBodyLimit       = 64 << 10
	adminConfigBodyLimit = 1 << 20
	// adminSessionMaxAge matches the auth service's default absolute session
	// lifetime; the cookie expires with the session.
	adminSessionMaxAge = 7 * 24 * time.Hour
)

// AdminDeps are the dependencies of the /api/admin/ handler.
type AdminDeps struct {
	Auth       *auth.Service
	ConfigPath string // -config value (may be "")
	ConfigDir  string // -config-dir value (may be "")
	Reload     func() // called after a successful config save; the handler runs it in a goroutine
}

// adminHandler serves /api/admin/. Public routes (session, setup, login) are
// reachable without a principal; every other route is only mounted behind
// the admin session gate.
type adminHandler struct {
	deps     AdminDeps
	cfg      config.Config
	configMu sync.Mutex // serialises config saves
}

func newAdminHandler(deps AdminDeps, cfg config.Config) http.Handler {
	h := &adminHandler{deps: deps, cfg: cfg}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/admin/session", h.session)
	mux.HandleFunc("POST /api/admin/setup", h.setup)
	mux.HandleFunc("POST /api/admin/login", h.login)
	mux.HandleFunc("POST /api/admin/logout", h.logout)
	mux.HandleFunc("POST /api/admin/password", h.changePassword)
	mux.HandleFunc("POST /api/admin/2fa/begin", h.twoFactorBegin)
	mux.HandleFunc("POST /api/admin/2fa/confirm", h.twoFactorConfirm)
	mux.HandleFunc("POST /api/admin/2fa/disable", h.twoFactorDisable)
	mux.HandleFunc("GET /api/admin/keys", h.listKeys)
	mux.HandleFunc("POST /api/admin/keys", h.createKey)
	mux.HandleFunc("DELETE /api/admin/keys/{id}", h.revokeKey)
	mux.HandleFunc("GET /api/admin/config", h.listConfig)
	mux.HandleFunc("POST /api/admin/config/validate", h.validateConfig)
	mux.HandleFunc("PUT /api/admin/config", h.saveConfig)
	return mux
}

type adminSessionJSON struct {
	SetupRequired    bool   `json:"setupRequired"`
	Authenticated    bool   `json:"authenticated"`
	Username         string `json:"username"`
	TwoFactorEnabled bool   `json:"twoFactorEnabled"`
}

type adminLoginErrorJSON struct {
	Error        string `json:"error"`
	TOTPRequired bool   `json:"totpRequired"`
}

type adminAccountJSON struct {
	Username         string `json:"username"`
	TwoFactorEnabled bool   `json:"twoFactorEnabled"`
}

type adminKeyJSON struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	Models     []string   `json:"models"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
	RevokedAt  *time.Time `json:"revokedAt"`
	Active     bool       `json:"active"`
}

type adminCreatedKeyJSON struct {
	Key string `json:"key"`
	adminKeyJSON
}

func newAdminKeyJSON(info auth.KeyInfo) adminKeyJSON {
	return adminKeyJSON{
		ID:         info.ID,
		Name:       info.Name,
		Prefix:     info.Prefix,
		Models:     info.Models,
		CreatedAt:  info.CreatedAt,
		LastUsedAt: info.LastUsedAt,
		RevokedAt:  info.RevokedAt,
		Active:     info.RevokedAt == nil,
	}
}

func (h *adminHandler) session(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	status, err := h.deps.Auth.Status(ctx)
	if err != nil {
		writeAdminServiceError(w, err)
		return
	}
	resp := adminSessionJSON{SetupRequired: status.SetupRequired, TwoFactorEnabled: status.TwoFactorEnabled}
	if token := adminSessionToken(r); token != "" {
		if info, ok := h.deps.Auth.Session(ctx, token); ok {
			resp.Authenticated = true
			resp.Username = info.Username
			resp.TwoFactorEnabled = info.TwoFactorEnabled
		}
	}
	writeAdminJSON(w, http.StatusOK, resp)
}

func (h *adminHandler) setup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token    string `json:"token"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeAdminBody(w, r, adminBodyLimit, &req) {
		return
	}
	ctx := r.Context()
	token, err := h.deps.Auth.Setup(ctx, adminClientIP(r), req.Token, req.Username, req.Password)
	switch {
	case errors.Is(err, auth.ErrAlreadySetUp):
		writeAdminError(w, http.StatusConflict, "admin is already set up")
		return
	case errors.Is(err, auth.ErrSetupTokenInvalid):
		writeAdminError(w, http.StatusUnauthorized, "invalid setup token")
		return
	case err != nil:
		writeAdminServiceError(w, err)
		return
	}
	info, ok := h.deps.Auth.Session(ctx, token)
	if !ok {
		writeAdminInternal(w)
		return
	}
	setAdminSessionCookie(w, r, token)
	writeAdminJSON(w, http.StatusCreated, adminAccountJSON{Username: info.Username, TwoFactorEnabled: info.TwoFactorEnabled})
}

func (h *adminHandler) login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if !decodeAdminBody(w, r, adminBodyLimit, &req) {
		return
	}
	ctx := r.Context()
	token, err := h.deps.Auth.Login(ctx, adminClientIP(r), req.Username, req.Password, req.Code)
	switch {
	case errors.Is(err, auth.ErrTOTPRequired):
		writeAdminJSON(w, http.StatusUnauthorized, adminLoginErrorJSON{Error: "invalid credentials", TOTPRequired: true})
		return
	case errors.Is(err, auth.ErrInvalidCredentials), errors.Is(err, auth.ErrTOTPInvalid), errors.Is(err, auth.ErrNotSetUp):
		writeAdminError(w, http.StatusUnauthorized, "invalid credentials")
		return
	case err != nil:
		writeAdminServiceError(w, err)
		return
	}
	info, ok := h.deps.Auth.Session(ctx, token)
	if !ok {
		writeAdminInternal(w)
		return
	}
	setAdminSessionCookie(w, r, token)
	writeAdminJSON(w, http.StatusOK, adminAccountJSON{Username: info.Username, TwoFactorEnabled: info.TwoFactorEnabled})
}

func (h *adminHandler) logout(w http.ResponseWriter, r *http.Request) {
	if err := h.deps.Auth.Logout(r.Context(), adminSessionToken(r)); err != nil {
		writeAdminServiceError(w, err)
		return
	}
	clearAdminSessionCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
}

func (h *adminHandler) changePassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if !decodeAdminBody(w, r, adminBodyLimit, &req) {
		return
	}
	err := h.deps.Auth.ChangePassword(r.Context(), adminClientIP(r), adminSessionToken(r), req.CurrentPassword, req.NewPassword)
	if errors.Is(err, auth.ErrInvalidCredentials) {
		writeAdminError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if err != nil {
		writeAdminServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *adminHandler) twoFactorBegin(w http.ResponseWriter, r *http.Request) {
	enrollment, err := h.deps.Auth.BeginTwoFactor(r.Context())
	if errors.Is(err, auth.ErrTOTPAlreadyEnabled) {
		writeAdminError(w, http.StatusConflict, "two-factor authentication is already enabled")
		return
	}
	if err != nil {
		writeAdminServiceError(w, err)
		return
	}
	writeAdminJSON(w, http.StatusOK, map[string]string{
		"secret":     enrollment.Secret,
		"otpauthUri": enrollment.URI,
	})
}

func (h *adminHandler) twoFactorConfirm(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
	}
	if !decodeAdminBody(w, r, adminBodyLimit, &req) {
		return
	}
	codes, err := h.deps.Auth.ConfirmTwoFactor(r.Context(), adminClientIP(r), req.Code)
	switch {
	case errors.Is(err, auth.ErrTOTPInvalid):
		writeAdminError(w, http.StatusBadRequest, "invalid code")
		return
	case errors.Is(err, auth.ErrTOTPNotEnrolled):
		writeAdminError(w, http.StatusConflict, "two-factor setup has not been started")
		return
	case errors.Is(err, auth.ErrTOTPAlreadyEnabled):
		writeAdminError(w, http.StatusConflict, "two-factor authentication is already enabled")
		return
	case err != nil:
		writeAdminServiceError(w, err)
		return
	}
	writeAdminJSON(w, http.StatusOK, map[string][]string{"recoveryCodes": codes})
}

func (h *adminHandler) twoFactorDisable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if !decodeAdminBody(w, r, adminBodyLimit, &req) {
		return
	}
	err := h.deps.Auth.DisableTwoFactor(r.Context(), adminClientIP(r), req.Password, req.Code)
	switch {
	case errors.Is(err, auth.ErrInvalidCredentials), errors.Is(err, auth.ErrTOTPInvalid):
		writeAdminError(w, http.StatusUnauthorized, "invalid password or code")
		return
	case errors.Is(err, auth.ErrTOTPNotEnabled):
		writeAdminError(w, http.StatusConflict, "two-factor authentication is not enabled")
		return
	case err != nil:
		writeAdminServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *adminHandler) listKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := h.deps.Auth.ListKeys(r.Context())
	if err != nil {
		writeAdminServiceError(w, err)
		return
	}
	items := make([]adminKeyJSON, 0, len(keys))
	for _, k := range keys {
		items = append(items, newAdminKeyJSON(k))
	}
	writeAdminJSON(w, http.StatusOK, map[string][]adminKeyJSON{"keys": items})
}

func (h *adminHandler) createKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name   string   `json:"name"`
		Models []string `json:"models"`
	}
	if !decodeAdminBody(w, r, adminBodyLimit, &req) {
		return
	}
	if err := validateKeyModels(h.cfg, req.Models); err != nil {
		writeAdminError(w, http.StatusBadRequest, err.Error())
		return
	}
	plaintext, info, err := h.deps.Auth.CreateKey(r.Context(), req.Name, req.Models)
	if err != nil {
		writeAdminServiceError(w, err)
		return
	}
	writeAdminJSON(w, http.StatusCreated, adminCreatedKeyJSON{Key: plaintext, adminKeyJSON: newAdminKeyJSON(info)})
}

func (h *adminHandler) revokeKey(w http.ResponseWriter, r *http.Request) {
	err := h.deps.Auth.RevokeKey(r.Context(), r.PathValue("id"))
	if errors.Is(err, auth.ErrKeyNotFound) {
		writeAdminError(w, http.StatusNotFound, "key not found")
		return
	}
	if err != nil {
		writeAdminServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// validateKeyModels accepts "*" alone, or a non-empty list of model IDs,
// aliases, selector IDs, profile names and peer model names from cfg.
func validateKeyModels(cfg config.Config, models []string) error {
	if len(models) == 0 {
		return errors.New("models must not be empty")
	}
	if slices.Contains(models, "*") {
		if len(models) > 1 {
			return errors.New(`"*" cannot be combined with other models`)
		}
		return nil
	}
	for _, name := range models {
		if !isKnownKeyModel(cfg, name) {
			return fmt.Errorf("unknown model, alias, selector, or profile: %s", name)
		}
	}
	return nil
}

func isKnownKeyModel(cfg config.Config, name string) bool {
	if _, ok := cfg.Models[name]; ok {
		return true
	}
	if _, ok := cfg.Selectors[name]; ok {
		return true
	}
	if _, ok := cfg.Profiles[name]; ok {
		return true
	}
	for _, mc := range cfg.Models {
		if slices.Contains(mc.Aliases, name) {
			return true
		}
	}
	return false
}

// decodeAdminBody reads a JSON object with unknown fields rejected. It writes
// the error response and returns false when the body is invalid.
func decodeAdminBody(w http.ResponseWriter, r *http.Request, limit int64, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeAdminError(w, http.StatusRequestEntityTooLarge, "request body too large")
		} else {
			writeAdminError(w, http.StatusBadRequest, "invalid request body")
		}
		return false
	}
	return true
}

func writeAdminJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeAdminError(w http.ResponseWriter, status int, msg string) {
	writeAdminJSON(w, status, map[string]string{"error": msg})
}

func writeAdminInternal(w http.ResponseWriter) {
	writeAdminError(w, http.StatusInternalServerError, "internal error")
}

// writeAdminServiceError answers errors shared by several endpoints. Anything
// it does not recognise is answered as an internal error.
func writeAdminServiceError(w http.ResponseWriter, err error) {
	var throttled auth.ThrottledError
	switch {
	case errors.As(err, &throttled):
		retry := int(math.Ceil(throttled.RetryAfter.Seconds()))
		if retry < 1 {
			retry = 1
		}
		w.Header().Set("Retry-After", strconv.Itoa(retry))
		writeAdminError(w, http.StatusTooManyRequests, "too many attempts; try again later")
	case errors.Is(err, auth.ErrPasswordTooShort):
		writeAdminError(w, http.StatusBadRequest, fmt.Sprintf("password must be at least %d characters", auth.MinPasswordLength))
	case errors.Is(err, auth.ErrInvalidUsername):
		writeAdminError(w, http.StatusBadRequest, "invalid username")
	case errors.Is(err, auth.ErrKeyNameInvalid):
		writeAdminError(w, http.StatusBadRequest, "key name must be 1-64 characters")
	default:
		writeAdminInternal(w)
	}
}

// adminClientIP is the throttling key: the host part of RemoteAddr.
func adminClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func adminSessionToken(r *http.Request) string {
	c, err := r.Cookie(auth.SessionCookieName)
	if err != nil {
		return ""
	}
	return c.Value
}

func setAdminSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, adminSessionCookie(r, token, int(adminSessionMaxAge.Seconds())))
}

func clearAdminSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, adminSessionCookie(r, "", -1))
}

func adminSessionCookie(r *http.Request, value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     auth.SessionCookieName,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   isSecureRequest(r),
		SameSite: http.SameSiteStrictMode,
		MaxAge:   maxAge,
	}
}

func isSecureRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https")
}
