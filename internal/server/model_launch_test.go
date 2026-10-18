package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mostlygeek/llama-swap/internal/auth"
	"github.com/mostlygeek/llama-swap/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newLaunchTestServer(t *testing.T, cfg config.Config) (*Server, string) {
	t.Helper()
	s, svc := newAuthTestServer(t, cfg, "m1")
	token, ok := svc.SetupToken()
	require.True(t, ok)
	_, err := svc.Setup(context.Background(), "127.0.0.1", token, "admin", "correct-horse-battery")
	require.NoError(t, err)
	session, err := svc.Login(context.Background(), "127.0.0.1", "admin", "correct-horse-battery", "")
	require.NoError(t, err)
	return s, session
}

func TestServer_ModelLaunchOptions(t *testing.T) {
	cfg := config.Config{
		Models: map[string]config.ModelConfig{
			"m1": {
				Cmd:              "serve --port ${PORT}",
				CmdStop:          "kill ${PID}",
				Env:              []string{"A=1", "B=2"},
				Proxy:            "http://localhost:8080",
				CheckEndpoint:    "/health",
				UnloadAfter:      900,
				UnloadTimeout:    30,
				ConcurrencyLimit: 2,
				Aliases:          []string{"alias1"},
				Unlisted:         true,
			},
		},
	}
	s, session := newLaunchTestServer(t, cfg)

	t.Run("returns the configured launch options", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/api/models/launch/m1", nil)
		r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: session})
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		require.Equal(t, http.StatusOK, w.Code)

		var body apiModelLaunch
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		assert.Equal(t, "m1", body.ID)
		assert.Equal(t, "serve --port ${PORT}", body.Cmd)
		assert.Equal(t, "kill ${PID}", body.CmdStop)
		assert.Equal(t, []string{"A=1", "B=2"}, body.Env)
		assert.Equal(t, "http://localhost:8080", body.Proxy)
		assert.Equal(t, "/health", body.CheckEndpoint)
		assert.Equal(t, 900, body.TTL)
		assert.Equal(t, 30, body.UnloadTimeout)
		assert.Equal(t, 2, body.ConcurrencyLimit)
		assert.Equal(t, []string{"alias1"}, body.Aliases)
		assert.True(t, body.Unlisted)
	})

	t.Run("without a session it is rejected", func(t *testing.T) {
		w := httptest.NewRecorder()
		s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/models/launch/m1", nil))
		assert.Equal(t, http.StatusUnauthorized, w.Code)
		assert.NotContains(t, w.Body.String(), "serve --port")
	})

	t.Run("unknown model is a 404", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/api/models/launch/nope", nil)
		r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: session})
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		assert.Equal(t, http.StatusNotFound, w.Code)
	})
}
