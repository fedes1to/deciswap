package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mostlygeek/llama-swap/internal/auth"
	"github.com/mostlygeek/llama-swap/internal/config"
	"github.com/mostlygeek/llama-swap/internal/store/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServer_RequestContextMiddleware(t *testing.T) {
	cfg := config.Config{
		Models: map[string]config.ModelConfig{
			"llama3": {},
		},
	}

	final := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	mw := CreateRequestContextMiddleware(cfg)

	t.Run("known model passes through", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"llama3"}`))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mw(final).ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Errorf("status = %d, want 200", w.Code)
		}
	})

	t.Run("missing model returns 404", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mw(final).ServeHTTP(w, r)
		if w.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", w.Code)
		}
	})
}

// newAuthTestServer builds a routed Server with an auth service on an
// in-memory store. The stub local router serves the given models.
func newAuthTestServer(t *testing.T, cfg config.Config, models ...string) (*Server, *auth.Service) {
	t.Helper()
	st, err := sqlite.New("")
	require.NoError(t, err)
	t.Cleanup(func() { st.Close() })

	svc, err := auth.New(context.Background(), st.Auth(), auth.Options{})
	require.NoError(t, err)

	s := newTestServerWithConfig(cfg, newStubRouter(models, "ok"), newStubRouter(nil, ""))
	s.admin = AdminDeps{Auth: svc}
	s.routes()
	return s, svc
}

func TestAuth_ScopedKeyModelAccess(t *testing.T) {
	cfg := config.Config{
		Models: map[string]config.ModelConfig{
			"m1": {Cmd: "x"},
			"m2": {Cmd: "x"},
		},
	}
	s, svc := newAuthTestServer(t, cfg, "m1", "m2")
	key, _, err := svc.CreateKey(context.Background(), "scoped", []string{"m1"})
	require.NoError(t, err)

	tests := []struct {
		name   string
		model  string
		status int
	}{
		{"allowed model", "m1", http.StatusOK},
		{"denied model", "m2", http.StatusForbidden},
		{"unknown model", "nope", http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"`+tt.model+`"}`))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Authorization", "Bearer "+key)
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			assert.Equal(t, tt.status, w.Code)
		})
	}
}

func TestAuth_ScopedKeyAutoSelector(t *testing.T) {
	cfg := config.Config{
		Models: map[string]config.ModelConfig{
			"m1": {Cmd: "x"},
		},
		Selectors: map[string]config.SelectorConfig{
			"auto": {Strategy: "warm", Targets: []string{"m1"}},
		},
	}
	s, svc := newAuthTestServer(t, cfg, "m1")
	key, _, err := svc.CreateKey(context.Background(), "auto-key", []string{"auto"})
	require.NoError(t, err)

	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"auto"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+key)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	assert.NotEqual(t, http.StatusForbidden, w.Code, "auto selector listed on the key must be allowed")
	assert.NotEqual(t, http.StatusUnauthorized, w.Code)
}

func TestAuth_ModelsListFiltered(t *testing.T) {
	cfg := config.Config{
		Models: map[string]config.ModelConfig{
			"m1": {Cmd: "x"},
			"m2": {Cmd: "x"},
		},
	}
	s, svc := newAuthTestServer(t, cfg, "m1", "m2")
	key, _, err := svc.CreateKey(context.Background(), "scoped", []string{"m1"})
	require.NoError(t, err)

	r := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	r.Header.Set("Authorization", "Bearer "+key)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	require.Equal(t, http.StatusOK, w.Code)

	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	ids := make([]string, 0, len(body.Data))
	for _, d := range body.Data {
		ids = append(ids, d.ID)
	}
	assert.Equal(t, []string{"m1"}, ids)
}

func TestAuth_DBKeyDeniedOnOpsAndAPI(t *testing.T) {
	cfg := config.Config{
		Models: map[string]config.ModelConfig{"m1": {Cmd: "x"}},
	}
	s, svc := newAuthTestServer(t, cfg, "m1")
	key, _, err := svc.CreateKey(context.Background(), "all", []string{"*"})
	require.NoError(t, err)

	tests := []struct {
		name   string
		method string
		path   string
	}{
		{"api version", http.MethodGet, "/api/version"},
		{"logs", http.MethodGet, "/logs"},
		{"metrics", http.MethodGet, "/metrics"},
		{"running", http.MethodGet, "/running"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(tt.method, tt.path, nil)
			r.Header.Set("Authorization", "Bearer "+key)
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			assert.Equal(t, http.StatusForbidden, w.Code)
		})
	}
}

func TestAuth_LegacyKeyFullAccess(t *testing.T) {
	cfg := config.Config{
		RequiredAPIKeys: []string{"secret"},
		Models: map[string]config.ModelConfig{
			"m1": {Cmd: "x"},
			"m2": {Cmd: "x"},
		},
	}
	s, _ := newAuthTestServer(t, cfg, "m1", "m2")

	r := httptest.NewRequest(http.MethodGet, "/logs", nil)
	r.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	assert.Equal(t, http.StatusOK, w.Code)

	r = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	r.Header.Set("Authorization", "Bearer secret")
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"m1"`)
	assert.Contains(t, w.Body.String(), `"m2"`)
}

func TestAuth_CSRFRequiredOnCookieUnsafe(t *testing.T) {
	cfg := config.Config{
		Models: map[string]config.ModelConfig{"m1": {Cmd: "x"}},
	}
	s, svc := newAuthTestServer(t, cfg, "m1")
	ctx := context.Background()
	token, ok := svc.SetupToken()
	require.True(t, ok)
	_, err := svc.Setup(ctx, "127.0.0.1", token, "admin", "correct-horse-battery")
	require.NoError(t, err)
	sessionToken, err := svc.Login(ctx, "127.0.0.1", "admin", "correct-horse-battery", "")
	require.NoError(t, err)

	tests := []struct {
		name   string
		header string
		want   int
	}{
		{"missing csrf header", "", http.StatusForbidden},
		{"csrf header present", auth.CSRFHeaderValue, http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/api/models/unload", nil)
			r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: sessionToken})
			if tt.header != "" {
				r.Header.Set(auth.CSRFHeader, tt.header)
			}
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			assert.Equal(t, tt.want, w.Code)
		})
	}
}

func TestAuth_UIGateWhenAdminExists(t *testing.T) {
	cfg := config.Config{
		Models: map[string]config.ModelConfig{"m1": {Cmd: "x"}},
	}
	s, svc := newAuthTestServer(t, cfg, "m1")
	token, ok := svc.SetupToken()
	require.True(t, ok)
	_, err := svc.Setup(context.Background(), "127.0.0.1", token, "admin", "correct-horse-battery")
	require.NoError(t, err)

	r := httptest.NewRequest(http.MethodGet, "/ui/", nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	// The gate must let the SPA load (it renders the login page itself). The
	// untagged test build has no embedded UI, so the handler's 404 is expected;
	// what matters is that the gate neither returns 401 nor triggers Basic auth.
	assert.NotEqual(t, http.StatusUnauthorized, w.Code)
	assert.Empty(t, w.Header().Get("WWW-Authenticate"), "SPA gate must not trigger the Basic prompt")
}
