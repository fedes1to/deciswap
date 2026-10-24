package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mostlygeek/llama-swap/internal/auth"
	"github.com/mostlygeek/llama-swap/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func loadSelectorTestConfig(t *testing.T) config.Config {
	t.Helper()
	cfg, err := config.LoadConfigFromReader(strings.NewReader(`
models:
  a:
    cmd: echo ${PORT}
  b:
    cmd: echo ${PORT}
groups:
  shared:
    swap: false
    members: [a, b]
selectors:
  ship:
    strategy: spillover
    targets: [a, b]
    settings:
      spillover: 2
    name: Ship
    description: Spill onto the remote model
    unlisted: true
    metadata:
      family: chat
  auto:
    strategy: decision
    routes:
      general:
        model: a
        description: Everyday prompts
    settings:
      decider: b
      default: a
`))
	require.NoError(t, err)
	return cfg
}

func TestServer_SelectorsReturnsConfig(t *testing.T) {
	s, session := newLaunchTestServer(t, loadSelectorTestConfig(t))

	r := httptest.NewRequest(http.MethodGet, "/api/selectors", nil)
	r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: session})
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))

	var body struct {
		Selectors []apiSelector `json:"selectors"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Len(t, body.Selectors, 2)
	assert.Equal(t, "auto", body.Selectors[0].ID)
	assert.Equal(t, "ship", body.Selectors[1].ID)

	auto := body.Selectors[0]
	assert.Equal(t, config.SelectorStrategyDecision, auto.Strategy)
	assert.Equal(t, "b", auto.Settings.Decider)
	assert.Equal(t, "a", auto.Settings.Default)
	assert.Equal(t, config.SelectorModeRequest, auto.Settings.Mode)
	assert.Equal(t, config.DefaultDecisionTimeout, auto.Settings.Timeout)
	assert.Equal(t, []string{"a"}, auto.Targets)
	require.Contains(t, auto.Routes, "general")
	assert.Equal(t, "a", auto.Routes["general"].Model)
	assert.Equal(t, "Everyday prompts", auto.Routes["general"].Description)

	ship := body.Selectors[1]
	assert.Equal(t, "Ship", ship.Name)
	assert.Equal(t, "Spill onto the remote model", ship.Description)
	assert.Equal(t, config.SelectorStrategySpillover, ship.Strategy)
	assert.True(t, ship.Unlisted)
	assert.Equal(t, []string{"a", "b"}, ship.Targets)
	assert.Equal(t, 2, ship.Settings.Spillover)
	assert.Equal(t, "chat", ship.Metadata["family"])
}

func TestServer_SelectorsRequireAdminSession(t *testing.T) {
	s, svc := newAuthTestServer(t, loadSelectorTestConfig(t), "a", "b")
	key, _, err := svc.CreateKey(t.Context(), "ops", []string{"*"})
	require.NoError(t, err)

	r := httptest.NewRequest(http.MethodGet, "/api/selectors", nil)
	r.Header.Set("Authorization", "Bearer "+key)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.NotContains(t, w.Body.String(), "decider")

	w = httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/selectors", nil))
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}
