package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"slices"
	"strings"

	"github.com/mostlygeek/llama-swap/internal/auth"
	"github.com/mostlygeek/llama-swap/internal/chain"
	"github.com/mostlygeek/llama-swap/internal/config"
	"github.com/mostlygeek/llama-swap/internal/swaputil"
)

// principalKind is how a request authenticated (contract 4.2).
type principalKind int

const (
	principalNone   principalKind = iota
	principalLegacy               // an entry of the config's apiKeys
	principalKey                  // a scoped API key from the auth store
	principalAdmin                // a valid admin session cookie
)

// principal is resolved once per request. key is set only for principalKey.
type principal struct {
	kind principalKind
	key  auth.KeyInfo
}

type principalContextKey struct{}

func principalFrom(ctx context.Context) principal {
	p, _ := ctx.Value(principalContextKey{}).(principal)
	return p
}

// routeClass groups routes that share an access rule (contract 4.3).
type routeClass int

const (
	classUI            routeClass = iota
	classAdmin                    // /api/admin/*
	classOps                      // /api/* (non-admin), /logs, /metrics, ...
	classInference                // model-dispatched and /upstream/* routes
	classInferenceList            // /v1/models and /models
)

func (s *Server) adminConfigured() bool {
	return s.admin.Auth != nil && s.admin.Auth.AdminConfigured()
}

func (s *Server) keysConfigured() bool {
	return s.admin.Auth != nil && s.admin.Auth.KeysConfigured()
}

// authEnforced reports whether any credential exists. When none does, every
// route is open except /api/admin/*.
func (s *Server) authEnforced() bool {
	return len(s.cfg.RequiredAPIKeys) > 0 || s.keysConfigured() || s.adminConfigured()
}

// resolvePrincipal finds the caller: an admin session cookie, else a legacy
// apiKeys entry (constant time), else a scoped key from the auth store.
func (s *Server) resolvePrincipal(r *http.Request) principal {
	if s.admin.Auth != nil {
		if c, err := r.Cookie(auth.SessionCookieName); err == nil {
			if _, ok := s.admin.Auth.Session(r.Context(), c.Value); ok {
				return principal{kind: principalAdmin}
			}
		}
	}

	provided := swaputil.ExtractAPIKey(r)
	if provided == "" {
		return principal{}
	}
	for _, key := range s.cfg.RequiredAPIKeys {
		if subtle.ConstantTimeCompare([]byte(provided), []byte(key)) == 1 {
			return principal{kind: principalLegacy}
		}
	}
	if s.admin.Auth != nil {
		if info, ok := s.admin.Auth.ResolveKey(provided); ok {
			return principal{kind: principalKey, key: info}
		}
	}
	return principal{}
}

// principalMiddleware attaches the caller to the request context. A cookie
// session on an unsafe method must carry the CSRF header.
func (s *Server) principalMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := s.resolvePrincipal(r)
		if p.kind == principalAdmin && isUnsafeMethod(r.Method) &&
			r.Header.Get(auth.CSRFHeader) != auth.CSRFHeaderValue {
			writeAccessError(w, http.StatusForbidden, "missing CSRF header", false)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalContextKey{}, p)))
	})
}

// accessGate enforces the access decision table for one route class.
func (s *Server) accessGate(class routeClass) chain.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if status, message, basic := s.accessDenial(class, r); status != 0 {
				writeAccessError(w, status, message, basic)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// accessDenial returns the status to reject with, or 0 to allow the request.
// basic adds a WWW-Authenticate Basic challenge to the rejection.
func (s *Server) accessDenial(class routeClass, r *http.Request) (status int, message string, basic bool) {
	p := principalFrom(r.Context())

	if class == classAdmin {
		if isPublicAdminRoute(r) || p.kind == principalAdmin {
			return 0, "", false
		}
		if p.kind == principalNone {
			return http.StatusUnauthorized, "unauthorized: admin session required", false
		}
		return http.StatusForbidden, "forbidden: admin session required", false
	}

	switch p.kind {
	case principalAdmin, principalLegacy:
		return 0, "", false
	case principalKey:
		switch class {
		case classInferenceList:
			return 0, "", false
		case classInference:
			model, ok := requestedModel(s.cfg, r)
			if ok && keyAllowsModel(s.cfg, p.key.Models, model) {
				return 0, "", false
			}
			return http.StatusForbidden, "forbidden: API key not permitted for this model", false
		}
		return http.StatusForbidden, "forbidden: API key not permitted for this route", false
	}

	// No principal.
	if class == classUI {
		if !s.authEnforced() || s.adminConfigured() {
			return 0, "", false
		}
		return http.StatusUnauthorized, "unauthorized: invalid or missing API key", len(s.cfg.RequiredAPIKeys) > 0
	}
	if !s.authEnforced() {
		return 0, "", false
	}
	if s.adminConfigured() {
		// A session cookie is the only credential the admin account accepts,
		// and a browser cannot turn a Basic challenge into one. Challenging
		// anyway makes the UI pop a credential dialog that nothing can
		// satisfy; the client-side gate sends the caller to the login form
		// instead.
		return http.StatusUnauthorized, "unauthorized: admin login required", false
	}
	return http.StatusUnauthorized, "unauthorized: invalid or missing API key", true
}

// isPublicAdminRoute reports the /api/admin/ routes that need no session.
func isPublicAdminRoute(r *http.Request) bool {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/admin/session":
		return true
	case r.Method == http.MethodPost && (r.URL.Path == "/api/admin/setup" || r.URL.Path == "/api/admin/login"):
		return true
	}
	return false
}

// requestedModel returns the model a request asks for, read before any
// profile, selector, or rewrite. /upstream/<model>/... takes it from the path.
func requestedModel(cfg config.Config, r *http.Request) (string, bool) {
	if strings.HasPrefix(r.URL.Path, "/upstream/") {
		name, _, _, found := swaputil.FindModelInPath(cfg, "/"+r.PathValue("upstreamPath"))
		return name, found
	}
	model, err := swaputil.ExtractModel(r)
	return model, err == nil && model != ""
}

// keyAllowsModel applies contract 4.5: "*" allows everything, otherwise the
// requested name or its configured base model must be listed.
func keyAllowsModel(cfg config.Config, models []string, name string) bool {
	if slices.Contains(models, "*") || slices.Contains(models, name) {
		return true
	}
	base, found := cfg.ResolveBaseModel(name)
	return found && slices.Contains(models, base)
}

func isUnsafeMethod(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

func writeAccessError(w http.ResponseWriter, status int, message string, basic bool) {
	if basic {
		w.Header().Set("WWW-Authenticate", `Basic realm="llama-swap"`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}

// CreateRequestContextMiddleware returns middleware that extracts model and
// auth info from the request into the context. Requests where no model can be
// identified are rejected with a 404, and bodies over the size cap with a 413.
//
// JSON and multipart bodies without a Content-Length are first read here, so
// this is where their size cap is usually hit.
func CreateRequestContextMiddleware(cfg config.Config) chain.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r = markInflightStart(r)
			data, err := swaputil.FetchContext(r, cfg)
			if err != nil {
				if bodyErr := bodyTooLargeFrom(err); bodyErr != nil {
					swaputil.SendResponse(w, r, bodyErr.status, bodyErr.message)
					return
				}
				swaputil.SendError(w, r, swaputil.ErrNoModelInContext)
				return
			}
			_ = data
			next.ServeHTTP(w, r)
		})
	}
}
