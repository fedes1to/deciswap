package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mostlygeek/llama-swap/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sessionChat builds a chat request carrying a session header.
func sessionChat(model, text, session string) *http.Request {
	req := routedChat(model, text)
	if session != "" {
		req.Header.Set(config.DefaultSessionHeader, session)
	}
	return req
}

// configureSelector applies settings to the env's "auto" selector.
func configureSelector(t *testing.T, env *decisionTestEnv, mutate func(*config.SelectorSettings)) {
	t.Helper()
	selector := env.server.cfg.Selectors["auto"]
	if selector.Settings.SessionHeader == "" {
		selector.Settings.SessionHeader = config.DefaultSessionHeader
	}
	if selector.Settings.RefreshHeader == "" {
		selector.Settings.RefreshHeader = config.DefaultRefreshHeader
	}
	mutate(&selector.Settings)
	env.server.cfg.Selectors["auto"] = selector
}

// pickyAnswer answers with an explicit probability map.
func pickyAnswer(choice string, probabilities map[string]float64) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		confidence := probabilities[choice]
		w.Header().Set("Content-Type", "application/json")
		body, _ := json.Marshal(map[string]any{
			"answers": map[string]any{
				"route": map[string]any{
					"choice":        choice,
					"confidence":    confidence,
					"probabilities": probabilities,
				},
			},
		})
		w.Write(body)
	}
}

func TestSelector_DecisionStickyPinsSession(t *testing.T) {
	env := newDecisionTestEnv(t, systemOneAnswer("coding", 0.9))
	configureSelector(t, env, func(s *config.SelectorSettings) {
		s.Mode = config.SelectorModeSticky
		s.SessionTTL = time.Hour
	})

	first := httptest.NewRecorder()
	env.server.ServeHTTP(first, sessionChat("auto", "debug this", "s1"))
	assert.Equal(t, "opus-5.5", first.Header().Get(headerRoutedModel))
	assert.Equal(t, "decided", first.Header().Get(headerRoutedSession))

	second := httptest.NewRecorder()
	env.server.ServeHTTP(second, sessionChat("auto", "write a poem", "s1"))
	assert.Equal(t, "opus-5.5", second.Header().Get(headerRoutedModel), "session stays pinned")
	assert.Equal(t, "pinned", second.Header().Get(headerRoutedSession))
	assert.Equal(t, int32(1), env.deciderHits.Load(), "second turn must not call the decider")

	other := httptest.NewRecorder()
	env.server.ServeHTTP(other, sessionChat("auto", "hello", "s2"))
	assert.Equal(t, int32(2), env.deciderHits.Load(), "another session decides independently")
}

func TestSelector_DecisionStickyRefreshHeader(t *testing.T) {
	var choice atomic.Value
	choice.Store("coding")
	env := newDecisionTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
		systemOneAnswer(choice.Load().(string), 0.9)(w, r)
	})
	configureSelector(t, env, func(s *config.SelectorSettings) {
		s.Mode = config.SelectorModeSticky
		s.SessionTTL = time.Hour
	})

	env.server.ServeHTTP(httptest.NewRecorder(), sessionChat("auto", "first", "s1"))

	choice.Store("general")
	refresh := sessionChat("auto", "second", "s1")
	refresh.Header.Set(config.DefaultRefreshHeader, "1")
	w := httptest.NewRecorder()
	env.server.ServeHTTP(w, refresh)

	assert.Equal(t, "fable-5.1", w.Header().Get(headerRoutedModel))
	assert.Equal(t, int32(2), env.deciderHits.Load(), "refresh header forces a new decision")
}

func TestSelector_DecisionStickyWithoutSessionIsStateless(t *testing.T) {
	env := newDecisionTestEnv(t, systemOneAnswer("coding", 0.9))
	configureSelector(t, env, func(s *config.SelectorSettings) {
		s.Mode = config.SelectorModeSticky
		s.SessionTTL = time.Hour
	})

	env.server.ServeHTTP(httptest.NewRecorder(), routedChat("auto", "first"))
	env.server.ServeHTTP(httptest.NewRecorder(), routedChat("auto", "second"))
	assert.Equal(t, int32(2), env.deciderHits.Load(), "no session key means per-request decisions")
}

func TestSelector_DecisionAdaptiveSwitchesAfterDwell(t *testing.T) {
	var choice atomic.Value
	choice.Store("coding")
	env := newDecisionTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
		if choice.Load().(string) == "coding" {
			pickyAnswer("coding", map[string]float64{"coding": 0.9, "general": 0.05})(w, r)
			return
		}
		pickyAnswer("general", map[string]float64{"coding": 0.1, "general": 0.8})(w, r)
	})
	configureSelector(t, env, func(s *config.SelectorSettings) {
		s.Mode = config.SelectorModeAdaptive
		s.SessionTTL = time.Hour
		s.MinDwell = 0
		s.SwitchMargin = 0.2
		s.SwitchConfidence = 0.5
	})

	env.server.ServeHTTP(httptest.NewRecorder(), sessionChat("auto", "debug this", "s1"))
	choice.Store("general")

	w := httptest.NewRecorder()
	env.server.ServeHTTP(w, sessionChat("auto", "write a poem", "s1"))
	assert.Equal(t, "fable-5.1", w.Header().Get(headerRoutedModel))
	assert.Equal(t, "switched", w.Header().Get(headerRoutedSession))
	assert.Equal(t, int32(2), env.deciderHits.Load())
}

func TestSelector_DecisionAdaptiveHoldsWithinDwell(t *testing.T) {
	var choice atomic.Value
	choice.Store("coding")
	env := newDecisionTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
		if choice.Load().(string) == "coding" {
			pickyAnswer("coding", map[string]float64{"coding": 0.9, "general": 0.05})(w, r)
			return
		}
		pickyAnswer("general", map[string]float64{"coding": 0.1, "general": 0.8})(w, r)
	})
	configureSelector(t, env, func(s *config.SelectorSettings) {
		s.Mode = config.SelectorModeAdaptive
		s.SessionTTL = time.Hour
		s.MinDwell = time.Hour
	})

	env.server.ServeHTTP(httptest.NewRecorder(), sessionChat("auto", "debug this", "s1"))
	choice.Store("general")

	w := httptest.NewRecorder()
	env.server.ServeHTTP(w, sessionChat("auto", "write a poem", "s1"))
	assert.Equal(t, "opus-5.5", w.Header().Get(headerRoutedModel), "dwell prevents the switch")
	assert.Equal(t, "pinned", w.Header().Get(headerRoutedSession))
}

func TestSelector_DecisionAdaptiveMargin(t *testing.T) {
	var choice atomic.Value
	choice.Store("coding")
	env := newDecisionTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
		if choice.Load().(string) == "coding" {
			pickyAnswer("coding", map[string]float64{"coding": 0.9, "general": 0.05})(w, r)
			return
		}
		pickyAnswer("general", map[string]float64{"coding": 0.5, "general": 0.55})(w, r)
	})
	configureSelector(t, env, func(s *config.SelectorSettings) {
		s.Mode = config.SelectorModeAdaptive
		s.SessionTTL = time.Hour
		s.MinDwell = 0
		s.SwitchMargin = 0.25
		s.SwitchConfidence = 0.5
	})

	env.server.ServeHTTP(httptest.NewRecorder(), sessionChat("auto", "debug this", "s1"))
	choice.Store("general")

	w := httptest.NewRecorder()
	env.server.ServeHTTP(w, sessionChat("auto", "write a poem", "s1"))
	assert.Equal(t, "opus-5.5", w.Header().Get(headerRoutedModel), "margin is too small to switch")
}

func TestSelector_DecisionStickyUnloadsDecider(t *testing.T) {
	env := newDecisionTestEnv(t, systemOneAnswer("coding", 0.9))
	configureSelector(t, env, func(s *config.SelectorSettings) {
		s.Mode = config.SelectorModeSticky
		s.SessionTTL = time.Hour
		s.UnloadDecider = true
	})

	env.server.ServeHTTP(httptest.NewRecorder(), sessionChat("auto", "debug this", "s1"))
	local := env.server.local.(*stubRouter)
	require.Eventually(t, func() bool { return local.unloadCalls.Load() == 1 }, 3*time.Second, 20*time.Millisecond)
	assert.Equal(t, []string{"kev"}, local.unloadModels)
}

func TestAPIDecisionRefreshForcesNextDecision(t *testing.T) {
	var choice atomic.Value
	choice.Store("coding")
	env := newDecisionTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
		systemOneAnswer(choice.Load().(string), 0.9)(w, r)
	})
	configureSelector(t, env, func(s *config.SelectorSettings) {
		s.Mode = config.SelectorModeSticky
		s.SessionTTL = time.Hour
	})

	env.server.ServeHTTP(httptest.NewRecorder(), sessionChat("auto", "first", "s1"))
	choice.Store("general")

	body := strings.NewReader(`{"selector":"auto","session":"s1"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/decision/refresh", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	env.server.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	w := httptest.NewRecorder()
	env.server.ServeHTTP(w, sessionChat("auto", "second", "s1"))
	assert.Equal(t, "fable-5.1", w.Header().Get(headerRoutedModel))
	assert.Equal(t, "decided", w.Header().Get(headerRoutedSession))
}
