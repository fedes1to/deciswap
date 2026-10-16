package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mostlygeek/llama-swap/internal/auth"
	"github.com/mostlygeek/llama-swap/internal/config"
	"github.com/mostlygeek/llama-swap/internal/store/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newAuthDecisionServer builds a routed Server with an auth store, a decision
// selector "auto" (general -> fable-5.1, coding -> opus-5.5), and an unlisted
// decider "kev". The stub router answers the decider with the given choice
// and answers targets with their name, so tests can see who served a request.
func newAuthDecisionServer(t *testing.T, choice string) (*Server, *auth.Service) {
	t.Helper()
	cfg := config.Config{
		Models: map[string]config.ModelConfig{
			"fable-5.1": {Cmd: "x", Unlisted: true},
			"opus-5.5":  {Cmd: "x", Unlisted: true},
			"kev":       {Cmd: "x", Unlisted: true},
		},
		Selectors: map[string]config.SelectorConfig{
			"auto": {
				Strategy: config.SelectorStrategyDecision,
				Routes: map[string]config.SelectorRoute{
					"general": {Model: "fable-5.1", Description: "general knowledge"},
					"coding":  {Model: "opus-5.5", Description: "code"},
				},
				Settings: config.SelectorSettings{
					Decider:       "kev",
					Default:       "fable-5.1",
					MinConfidence: 0.5,
					Timeout:       time.Second,
				},
				// Derived from routes at load time; set here as the loader would.
				Targets: []string{"fable-5.1", "opus-5.5"},
			},
		},
	}
	targets := &stubRouter{models: map[string]bool{"fable-5.1": true, "opus-5.5": true, "kev": true}}
	targets.serveHTTP = func(w http.ResponseWriter, r *http.Request) {
		model, _ := jsonModelField(r)
		if model == "kev" {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"answers":{"route":{"choice":"` + choice + `","confidence":0.9}}}`))
			return
		}
		w.Write([]byte(`{"answered_by":"` + model + `"}`))
	}

	st, err := sqlite.New("")
	require.NoError(t, err)
	t.Cleanup(func() { st.Close() })
	svc, err := auth.New(context.Background(), st.Auth(), auth.Options{})
	require.NoError(t, err)

	s := newTestServerWithConfig(cfg, targets, nil)
	s.admin = AdminDeps{Auth: svc}
	s.decisions = newDecisionCache(decisionCacheSize, decisionCacheTTL)
	s.routes()
	return s, svc
}

func TestAuth_ScopedKeyDecisionSelectorRoutesToHiddenTarget(t *testing.T) {
	s, svc := newAuthDecisionServer(t, "coding")
	key, _, err := svc.CreateKey(context.Background(), "auto-key", []string{"auto"})
	require.NoError(t, err)

	r := routedChat("auto", "debug this function")
	r.Header.Set("Authorization", "Bearer "+key)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)

	// The gate allows "auto" and the decider call is not gated, so the request
	// reaches opus-5.5 even though that target is not in the key's list.
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, "opus-5.5", w.Header().Get(headerRoutedModel))
	assert.Equal(t, "coding", w.Header().Get(headerRoutedLabel))
	assert.Contains(t, w.Body.String(), `"answered_by":"opus-5.5"`)
}

func TestAuth_ScopedKeyDirectTargetCallForbidden(t *testing.T) {
	s, svc := newAuthDecisionServer(t, "coding")
	key, _, err := svc.CreateKey(context.Background(), "auto-key", []string{"auto"})
	require.NoError(t, err)

	for _, model := range []string{"opus-5.5", "fable-5.1", "kev"} {
		r := routedChat(model, "hello")
		r.Header.Set("Authorization", "Bearer "+key)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		assert.Equal(t, http.StatusForbidden, w.Code, "model %s: %s", model, w.Body.String())
	}
}

func TestSelector_AuthScopedModelsListingHidesDecisionTargets(t *testing.T) {
	s, svc := newAuthDecisionServer(t, "coding")
	key, _, err := svc.CreateKey(context.Background(), "auto-key", []string{"auto"})
	require.NoError(t, err)

	r := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	r.Header.Set("Authorization", "Bearer "+key)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp struct {
		Data []struct {
			ID   string         `json:"id"`
			Meta map[string]any `json:"meta"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Data, 1, w.Body.String())
	assert.Equal(t, "auto", resp.Data[0].ID)

	meta, _ := resp.Data[0].Meta["llamaswap"].(map[string]any)
	assert.Equal(t, "selector", meta["type"])
	assert.Equal(t, "decision", meta["strategy"])
	assert.NotContains(t, meta, "targets")
	assert.NotContains(t, meta, "decider")
	assert.NotContains(t, meta, "default")
	assert.Equal(t, map[string]any{
		"coding":  "code",
		"general": "general knowledge",
	}, meta["routes"])

	// No hidden target or decider name may appear anywhere in the listing.
	body := w.Body.String()
	for _, hidden := range []string{"opus-5.5", "fable-5.1", "kev"} {
		assert.NotContains(t, body, hidden)
	}
}

func TestSelector_UnscopedModelsListingShowsDecisionFields(t *testing.T) {
	s, _ := newAuthDecisionServer(t, "coding")
	s.cfg.RequiredAPIKeys = []string{"legacy-key"}

	r := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	r.Header.Set("Authorization", "Bearer legacy-key")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"decider":"kev"`)
	assert.Contains(t, w.Body.String(), `"default":"fable-5.1"`)
	assert.Contains(t, w.Body.String(), `"model":"opus-5.5"`)
}

func TestAdmin_ConfigAcceptsDecisionSelectorAndRejectsTargets(t *testing.T) {
	env := newAdminTestEnv(t)
	cur := env.configFile(t, env.cfgPath)

	valid := adminTestConfig + `
selectors:
  auto:
    strategy: decision
    routes:
      general: { model: m1, description: general }
    settings: { decider: m1, default: m1 }
`
	rec := env.do(t, http.MethodPost, "/api/admin/config/validate", map[string]string{
		"path": env.cfgPath, "content": valid,
	})
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	invalid := adminTestConfig + `
selectors:
  auto:
    strategy: decision
    targets: [m1]
    routes:
      general: { model: m1 }
    settings: { decider: m1, default: m1 }
`
	rec = env.do(t, http.MethodPost, "/api/admin/config/validate", map[string]string{
		"path": env.cfgPath, "content": invalid,
	})
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.True(t, strings.Contains(rec.Body.String(), "targets is not supported"), rec.Body.String())

	rec = env.do(t, http.MethodPut, "/api/admin/config", map[string]string{
		"path": env.cfgPath, "content": invalid, "expectedHash": cur.Hash,
	})
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "targets is not supported")
	assert.Equal(t, int32(0), env.reloads.Load())

	rec = env.do(t, http.MethodPut, "/api/admin/config", map[string]string{
		"path": env.cfgPath, "content": valid, "expectedHash": cur.Hash,
	})
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}
