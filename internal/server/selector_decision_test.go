package server

import (
	"context"
	"encoding/json"
	"io"
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

func TestSelector_DecisionClassificationText(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		body    string
		want    string
		wantErr bool
	}{
		{
			name: "openai chat last user turn",
			path: "/v1/chat/completions",
			body: `{"messages":[
				{"role":"system","content":"be terse"},
				{"role":"user","content":"first question"},
				{"role":"assistant","content":"answer"},
				{"role":"user","content":"debug this go code"}]}`,
			want: "debug this go code",
		},
		{
			name: "tool result turns keep the human text",
			path: "/v1/chat/completions",
			body: `{"messages":[
				{"role":"user","content":"debug this go code"},
				{"role":"assistant","content":null,"tool_calls":[{"id":"1"}]},
				{"role":"tool","tool_call_id":"1","content":"exit status 1"}]}`,
			want: "debug this go code",
		},
		{
			name: "anthropic content blocks skip tool_result",
			path: "/v1/messages",
			body: `{"messages":[
				{"role":"user","content":[{"type":"text","text":"write a poem"}]},
				{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"x","input":{}}]},
				{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}]}`,
			want: "write a poem",
		},
		{
			name: "anthropic string content",
			path: "/v1/messages",
			body: `{"messages":[{"role":"user","content":"explain CVE-2024-1234"}]}`,
			want: "explain CVE-2024-1234",
		},
		{
			name: "system reminders are stripped",
			path: "/v1/messages",
			body: `{"messages":[{"role":"user","content":[
				{"type":"text","text":"<system-reminder>context noise</system-reminder>\nhow do I harden sshd?"}]}]}`,
			want: "how do I harden sshd?",
		},
		{
			name: "responses string input",
			path: "/v1/responses",
			body: `{"input":"summarize the paper"}`,
			want: "summarize the paper",
		},
		{
			name: "responses skips function_call_output",
			path: "/v1/responses",
			body: `{"input":[
				{"role":"user","content":[{"type":"input_text","text":"fix the bug"}]},
				{"type":"function_call_output","call_id":"c1","output":"stack trace"}]}`,
			want: "fix the bug",
		},
		{
			name: "completions prompt string",
			path: "/v1/completions",
			body: `{"prompt":"write a haiku"}`,
			want: "write a haiku",
		},
		{
			name: "completions prompt array uses last",
			path: "/v1/completions",
			body: `{"prompt":["first","second"]}`,
			want: "second",
		},
		{
			name:    "only tool results yields no text",
			path:    "/v1/chat/completions",
			body:    `{"messages":[{"role":"tool","content":"output"}]}`,
			wantErr: true,
		},
		{
			name:    "non chat route is not classified",
			path:    "/v1/embeddings",
			body:    `{"input":"hello"}`,
			wantErr: true,
		},
		{
			name:    "systemone route is not classified",
			path:    "/v1/systemone",
			body:    `{"state":"hello"}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := classificationText(tt.path, []byte(tt.body))
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestSelector_DecisionTruncatesHeadAndTail(t *testing.T) {
	long := strings.Repeat("a", decisionTextLimit) + strings.Repeat("b", decisionTextLimit)
	got := truncateDecisionText(long)

	assert.Less(t, len([]rune(got)), len([]rune(long)))
	assert.True(t, strings.HasPrefix(got, "aaaa"), "head is kept")
	assert.True(t, strings.HasSuffix(got, "bbbb"), "tail is kept")
}

func TestSelector_DecisionCacheBoundsAndExpires(t *testing.T) {
	c := newDecisionCache(2, time.Minute)
	now := time.Unix(0, 0)
	c.now = func() time.Time { return now }

	a := newDecisionKey("auto", "a")
	b := newDecisionKey("auto", "b")
	d := newDecisionKey("auto", "d")

	c.put(a, "general", 0.9, nil)
	c.put(b, "coding", 0.7, nil)
	_, ok := c.get(a) // touch a so b becomes least recently used
	require.True(t, ok)
	c.put(d, "security", 0.8, nil)

	assert.Len(t, c.entries, 2, "cache stays at its bound")
	_, ok = c.get(b)
	assert.False(t, ok, "least recently used entry is evicted")

	now = now.Add(2 * time.Minute)
	_, ok = c.get(a)
	assert.False(t, ok, "expired entry is not returned")
}

// decisionTestServer builds a server with a decision selector "auto" routing
// between two targets, and a stub decider whose System One answer is set per
// test. The decider and targets are stub routers, so no real process starts.
type decisionTestEnv struct {
	server      *Server
	deciderHits *atomic.Int32
	targetHits  map[string]*atomic.Int32
}

func newDecisionTestEnv(t *testing.T, decider func(w http.ResponseWriter, r *http.Request)) *decisionTestEnv {
	t.Helper()
	env := &decisionTestEnv{
		deciderHits: &atomic.Int32{},
		targetHits:  map[string]*atomic.Int32{"fable-5.1": {}, "opus-5.5": {}},
	}

	cfg := config.Config{
		Models: map[string]config.ModelConfig{
			"fable-5.1": {Cmd: "echo", Proxy: "http://localhost:0"},
			"opus-5.5":  {Cmd: "echo", Proxy: "http://localhost:0"},
			"kev":       {Cmd: "echo", Proxy: "http://localhost:0"},
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
				Targets: []string{"fable-5.1", "opus-5.5"},
			},
		},
	}

	targets := &stubRouter{models: map[string]bool{"fable-5.1": true, "opus-5.5": true, "kev": true}}
	targets.serveHTTP = func(w http.ResponseWriter, r *http.Request) {
		model, _ := jsonModelField(r)
		if model == "kev" {
			env.deciderHits.Add(1)
			decider(w, r)
			return
		}
		if hits, ok := env.targetHits[model]; ok {
			hits.Add(1)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"answered_by":"` + model + `"}`))
	}

	env.server = newTestServerWithConfig(cfg, targets, nil)
	// New() normally builds the cache; the test helper builds the Server directly.
	env.server.decisions = newDecisionCache(decisionCacheSize, decisionCacheTTL)
	return env
}

// jsonModelField reads the model field from a JSON request body, leaving the
// body readable for the handler that follows.
func jsonModelField(r *http.Request) (string, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return "", err
	}
	r.Body = io.NopCloser(strings.NewReader(string(body)))
	var m struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &m); err != nil {
		return "", err
	}
	return m.Model, nil
}

func systemOneAnswer(choice string, confidence float64) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"answers":{"route":{"choice":"` + choice + `","confidence":` + jsonFloat(confidence) + `}}}`))
	}
}

func jsonFloat(f float64) string {
	b, _ := json.Marshal(f)
	return string(b)
}

func routedChat(model, text string) *http.Request {
	body := `{"model":"` + model + `","messages":[{"role":"user","content":"` + text + `"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func TestSelector_DecisionRoutesByLabel(t *testing.T) {
	env := newDecisionTestEnv(t, systemOneAnswer("coding", 0.9))
	w := httptest.NewRecorder()
	env.server.ServeHTTP(w, routedChat("auto", "debug this"))

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "opus-5.5", w.Header().Get(headerRoutedModel))
	assert.Equal(t, "coding", w.Header().Get(headerRoutedLabel))
	assert.Empty(t, w.Header().Get(headerRoutedReason))
	assert.Contains(t, w.Body.String(), `"answered_by":"opus-5.5"`)
	assert.Equal(t, int32(1), env.targetHits["opus-5.5"].Load())
}

func TestSelector_DecisionLowConfidenceFallsBackToDefault(t *testing.T) {
	env := newDecisionTestEnv(t, systemOneAnswer("coding", 0.2))
	w := httptest.NewRecorder()
	env.server.ServeHTTP(w, routedChat("auto", "maybe code"))

	assert.Equal(t, "fable-5.1", w.Header().Get(headerRoutedModel))
	assert.Equal(t, decisionDefaultLabel, w.Header().Get(headerRoutedLabel))
	assert.Equal(t, "low-confidence", w.Header().Get(headerRoutedReason))
	assert.Equal(t, int32(1), env.targetHits["fable-5.1"].Load())
}

func TestSelector_DecisionFailuresFallBackToDefault(t *testing.T) {
	tests := []struct {
		name       string
		decider    func(w http.ResponseWriter, r *http.Request)
		wantReason string
	}{
		{
			name: "decider returns 500",
			decider: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
			},
			wantReason: "decider-status-500",
		},
		{
			name: "malformed json",
			decider: func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(`not json`))
			},
			wantReason: "decider-malformed",
		},
		{
			name:       "unknown label",
			decider:    systemOneAnswer("security", 0.9),
			wantReason: "unknown-label",
		},
		{
			name: "decider times out",
			decider: func(w http.ResponseWriter, r *http.Request) {
				select {
				case <-r.Context().Done():
				case <-time.After(3 * time.Second):
				}
			},
			wantReason: "timeout",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newDecisionTestEnv(t, tt.decider)
			if tt.name == "decider times out" {
				sel := env.server.cfg.Selectors["auto"]
				sel.Settings.Timeout = 50 * time.Millisecond
				env.server.cfg.Selectors["auto"] = sel
			}

			w := httptest.NewRecorder()
			env.server.ServeHTTP(w, routedChat("auto", "something"))

			assert.Equal(t, http.StatusOK, w.Code, "decider failure must not fail the client request")
			assert.Equal(t, "fable-5.1", w.Header().Get(headerRoutedModel))
			assert.Equal(t, tt.wantReason, w.Header().Get(headerRoutedReason))
		})
	}
}

func TestSelector_DecisionCacheAvoidsSecondDeciderCall(t *testing.T) {
	env := newDecisionTestEnv(t, systemOneAnswer("coding", 0.9))

	for range 3 {
		w := httptest.NewRecorder()
		env.server.ServeHTTP(w, routedChat("auto", "same tool loop text"))
		assert.Equal(t, "opus-5.5", w.Header().Get(headerRoutedModel))
	}

	assert.Equal(t, int32(1), env.deciderHits.Load(), "repeat text is decided once")
	assert.Equal(t, int32(3), env.targetHits["opus-5.5"].Load(), "every request still reaches the target")
}

func TestSelector_DecisionCacheHitKeepsConfidence(t *testing.T) {
	env := newDecisionTestEnv(t, systemOneAnswer("coding", 0.9))
	selector := env.server.cfg.Selectors["auto"]

	first := env.server.decide(context.Background(), "auto", selector, "cache me", false)
	second := env.server.decide(context.Background(), "auto", selector, "cache me", false)

	assert.False(t, first.cacheHit)
	assert.True(t, second.cacheHit)
	assert.Equal(t, first.confidence, second.confidence, "cache hit reports the decider's confidence")
	assert.Equal(t, "opus-5.5", second.target)
	assert.Equal(t, int32(1), env.deciderHits.Load(), "second call is served from cache")
}

func TestSelector_DecisionNonChatRouteUsesDefault(t *testing.T) {
	env := newDecisionTestEnv(t, systemOneAnswer("coding", 0.9))
	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings",
		strings.NewReader(`{"model":"auto","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	env.server.ServeHTTP(w, req)

	assert.Equal(t, "fable-5.1", w.Header().Get(headerRoutedModel))
	assert.Equal(t, "no-text", w.Header().Get(headerRoutedReason))
	assert.Equal(t, int32(0), env.deciderHits.Load(), "non-chat route must not call the decider")
}
