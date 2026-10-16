package server

import (
	"bytes"
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/mostlygeek/llama-swap/internal/config"
	"github.com/mostlygeek/llama-swap/internal/swaputil"
	"github.com/tidwall/gjson"
)

const (
	// decisionCacheSize bounds the number of remembered decisions across all
	// decision selectors. Tool-loop turns repeat the same classification text,
	// so a small cache covers the working set of an agent session.
	decisionCacheSize = 1024
	// decisionCacheTTL bounds how long a decision is reused. Expired entries
	// are re-decided on the next request for the same text.
	decisionCacheTTL = 10 * time.Minute
	// decisionTextLimit caps the classification text sent to the decider, in
	// runes. Long prompts keep the head and tail, since instructions often
	// appear at the start and the actual question at the end.
	decisionTextLimit = 4000
	// decisionMaxResponse caps how much of the decider response is read.
	decisionMaxResponse = 64 << 10

	// Response headers reported on every decision routed request.
	headerRoutedModel  = "X-Routed-Model"
	headerRoutedLabel  = "X-Routed-Label"
	headerRoutedReason = "X-Routed-Reason"

	decisionDefaultLabel        = "default"
	defaultDecisionInstructions = "Which specialist should handle this request?"
)

// systemRemindersPattern matches <system-reminder> blocks injected by agent
// harnesses. They change between turns and carry no signal about the request.
var systemRemindersPattern = regexp.MustCompile(`(?s)<system-reminder>.*?</system-reminder>`)

// decisionChatRoutes are the request paths whose body carries chat-style
// content. Any other route classifies to the default target without asking
// the decider.
var decisionChatRoutes = map[string]bool{
	"/v1/chat/completions": true,
	"/v1/messages":         true,
	"/v1/responses":        true,
	"/v1/completions":      true,
	"/v/chat/completions":  true,
	"/v/messages":          true,
	"/v/responses":         true,
	"/v/completions":       true,
}

// classificationText returns the text used to ask the decider about a request:
// the last human user turn of a chat-style body, with tool results and
// reminder blocks removed. An error means the request should use the default.
func classificationText(path string, body []byte) (string, error) {
	if !decisionChatRoutes[path] {
		return "", errors.New("route is not a chat request")
	}

	var text string
	switch path {
	case "/v1/completions", "/v/completions":
		text = completionPromptText(body)
	case "/v1/responses", "/v/responses":
		text = lastHumanResponsesInput(body)
	default:
		text = lastHumanMessageText(body)
	}

	text = strings.TrimSpace(systemRemindersPattern.ReplaceAllString(text, ""))
	if text == "" {
		return "", errors.New("no classification text")
	}
	return truncateDecisionText(text), nil
}

// completionPromptText reads a /v1/completions prompt, which is either a string
// or an array of strings. The last element is the most recent prompt.
func completionPromptText(body []byte) string {
	prompt := gjson.GetBytes(body, "prompt")
	if prompt.IsArray() {
		items := prompt.Array()
		if len(items) == 0 {
			return ""
		}
		return items[len(items)-1].String()
	}
	return prompt.String()
}

// lastHumanMessageText walks messages[] (OpenAI and Anthropic) from the end and
// returns the text of the last user message that is not a tool result.
func lastHumanMessageText(body []byte) string {
	messages := gjson.GetBytes(body, "messages").Array()
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.Get("role").String() != "user" {
			continue
		}
		content := msg.Get("content")
		if !content.IsArray() {
			if text := content.String(); text != "" {
				return text
			}
			continue
		}
		var parts []string
		for _, block := range content.Array() {
			if block.Get("type").String() == "text" {
				parts = append(parts, block.Get("text").String())
			}
		}
		if text := strings.Join(parts, "\n"); text != "" {
			return text
		}
	}
	return ""
}

// lastHumanResponsesInput returns the text of the last user item in a
// /v1/responses input. A plain string input is the user message itself.
// function_call_output items are skipped.
func lastHumanResponsesInput(body []byte) string {
	input := gjson.GetBytes(body, "input")
	if input.Type == gjson.String {
		return input.String()
	}
	items := input.Array()
	for i := len(items) - 1; i >= 0; i-- {
		item := items[i]
		if item.Get("type").String() == "function_call_output" {
			continue
		}
		if item.Get("role").String() != "user" {
			continue
		}
		content := item.Get("content")
		if content.Type == gjson.String {
			if text := content.String(); text != "" {
				return text
			}
			continue
		}
		var parts []string
		for _, block := range content.Array() {
			if t := block.Get("type").String(); t == "input_text" || t == "text" {
				parts = append(parts, block.Get("text").String())
			}
		}
		if text := strings.Join(parts, "\n"); text != "" {
			return text
		}
	}
	return ""
}

// truncateDecisionText keeps the first and last halves of text when it exceeds
// decisionTextLimit, so both the opening instructions and the closing question
// reach the decider. Cuts are made on rune boundaries.
func truncateDecisionText(text string) string {
	runes := []rune(text)
	if len(runes) <= decisionTextLimit {
		return text
	}
	half := decisionTextLimit / 2
	return string(runes[:half]) + "\n...\n" + string(runes[len(runes)-half:])
}

// decisionKey identifies a cached decision. The hash keeps keys fixed-size no
// matter how large the classification text is.
type decisionKey struct {
	selector string
	hash     [sha256.Size]byte
}

func newDecisionKey(selector, text string) decisionKey {
	return decisionKey{selector: selector, hash: sha256.Sum256([]byte(text))}
}

type decisionCacheEntry struct {
	key        decisionKey
	label      string
	confidence float64
	expires    time.Time
	elem       *list.Element
}

// decisionCache is a bounded LRU with a TTL. It is safe for concurrent use.
type decisionCache struct {
	mu      sync.Mutex
	max     int
	ttl     time.Duration
	entries map[decisionKey]*decisionCacheEntry
	order   *list.List
	now     func() time.Time
}

func newDecisionCache(max int, ttl time.Duration) *decisionCache {
	return &decisionCache{
		max:     max,
		ttl:     ttl,
		entries: make(map[decisionKey]*decisionCacheEntry, max),
		order:   list.New(),
		now:     time.Now,
	}
}

// get returns the cached label for key when present and unexpired.
func (c *decisionCache) get(key decisionKey) (decisionCacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return decisionCacheEntry{}, false
	}
	if !c.now().Before(entry.expires) {
		c.remove(entry)
		return decisionCacheEntry{}, false
	}
	c.order.MoveToFront(entry.elem)
	return *entry, true
}

// put stores label for key, evicting the least recently used entry when full.
func (c *decisionCache) put(key decisionKey, label string, confidence float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry, ok := c.entries[key]; ok {
		entry.label = label
		entry.confidence = confidence
		entry.expires = c.now().Add(c.ttl)
		c.order.MoveToFront(entry.elem)
		return
	}
	entry := &decisionCacheEntry{key: key, label: label, confidence: confidence, expires: c.now().Add(c.ttl)}
	entry.elem = c.order.PushFront(entry)
	c.entries[key] = entry
	for len(c.entries) > c.max {
		c.remove(c.order.Back().Value.(*decisionCacheEntry))
	}
}

func (c *decisionCache) remove(entry *decisionCacheEntry) {
	c.order.Remove(entry.elem)
	delete(c.entries, entry.key)
}

// decisionOutcome is the result of routing one request through a decision
// selector.
type decisionOutcome struct {
	target     string
	label      string
	reason     string
	confidence float64
	cacheHit   bool
}

// decisionQuestion is one System One question. Only "choice" questions are
// sent by the decision strategy.
type decisionQuestion struct {
	Type         string            `json:"type"`
	Instructions string            `json:"instructions"`
	Criteria     map[string]string `json:"criteria"`
}

type decisionRequest struct {
	Model     string                      `json:"model"`
	State     string                      `json:"state"`
	Questions map[string]decisionQuestion `json:"questions"`
}

type decisionResponse struct {
	Answers map[string]struct {
		Choice        string             `json:"choice"`
		Confidence    *float64           `json:"confidence"`
		Probabilities map[string]float64 `json:"probabilities"`
	} `json:"answers"`
}

// decide picks a route for a decision selector. It never fails the client
// request: any decider failure resolves to the selector default with a reason.
func (s *Server) decide(ctx context.Context, selectorID string, selector config.SelectorConfig, text string) decisionOutcome {
	fallback := func(reason string) decisionOutcome {
		return decisionOutcome{target: selector.Settings.Default, label: decisionDefaultLabel, reason: reason}
	}
	if text == "" {
		return fallback("no-text")
	}

	key := newDecisionKey(selectorID, text)
	if cached, ok := s.decisions.get(key); ok {
		outcome := routeForLabel(selector, cached.label)
		outcome.cacheHit = true
		outcome.confidence = cached.confidence
		return outcome
	}

	label, confidence, reason := s.askDecider(ctx, selector, text)
	if reason != "" {
		return fallback(reason)
	}
	s.decisions.put(key, label, confidence)
	outcome := routeForLabel(selector, label)
	outcome.confidence = confidence
	return outcome
}

// routeForLabel maps a label to its target.
func routeForLabel(selector config.SelectorConfig, label string) decisionOutcome {
	return decisionOutcome{target: selector.Routes[label].Model, label: label}
}

// askDecider sends the System One question to the decider through the local or
// peer dispatch path and returns the chosen label and confidence. A non-empty
// reason means the decision should fall back to the default.
func (s *Server) askDecider(ctx context.Context, selector config.SelectorConfig, text string) (string, float64, string) {
	criteria := make(map[string]string, len(selector.Routes))
	for label, route := range selector.Routes {
		criteria[label] = route.Description
	}
	instructions := selector.Settings.Instructions
	if instructions == "" {
		instructions = defaultDecisionInstructions
	}
	payload, err := json.Marshal(decisionRequest{
		Model: selector.Settings.Decider,
		State: text,
		Questions: map[string]decisionQuestion{
			"route": {Type: "choice", Instructions: instructions, Criteria: criteria},
		},
	})
	if err != nil {
		return "", 0, "encode-error"
	}

	callCtx, cancel := context.WithTimeout(ctx, selector.Settings.Timeout)
	defer cancel()

	// The decider is dispatched like a model request. The context carries the
	// decider model so FetchContext skips body parsing, and the local or peer
	// handler applies swapping and peer routing as it does for client requests.
	// The client's API key is not forwarded. The client was authenticated before
	// the selector ran, and a peer decider uses its own configured key.
	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, "/v1/systemone", bytes.NewReader(payload))
	if err != nil {
		return "", 0, "request-error"
	}
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(swaputil.SetContext(req.Context(), swaputil.ReqContextData{
		Model:    selector.Settings.Decider,
		ModelID:  selector.Settings.Decider,
		Metadata: make(map[string]string),
	}))

	rec := newDecisionRecorder()
	s.localPeerHandler(rec, req)

	if callCtx.Err() != nil {
		return "", 0, "timeout"
	}
	if rec.status != http.StatusOK {
		return "", 0, fmt.Sprintf("decider-status-%d", rec.status)
	}
	if rec.overflow {
		return "", 0, "decider-response-too-large"
	}

	var resp decisionResponse
	if err := json.Unmarshal(rec.body.Bytes(), &resp); err != nil {
		return "", 0, "decider-malformed"
	}
	answer, ok := resp.Answers["route"]
	if !ok {
		return "", 0, "decider-malformed"
	}
	if _, known := selector.Routes[answer.Choice]; !known {
		return "", 0, "unknown-label"
	}
	confidence := 1.0
	if answer.Confidence != nil {
		confidence = *answer.Confidence
	}
	if confidence < selector.Settings.MinConfidence {
		return "", confidence, "low-confidence"
	}
	return answer.Choice, confidence, ""
}

// decisionRecorder is an in-memory http.ResponseWriter for the decider call.
// It keeps at most decisionMaxResponse bytes and marks overflow.
type decisionRecorder struct {
	header   http.Header
	status   int
	body     bytes.Buffer
	overflow bool
}

func newDecisionRecorder() *decisionRecorder {
	return &decisionRecorder{header: make(http.Header), status: http.StatusOK}
}

func (r *decisionRecorder) Header() http.Header { return r.header }

func (r *decisionRecorder) WriteHeader(status int) { r.status = status }

func (r *decisionRecorder) Write(p []byte) (int, error) {
	remaining := decisionMaxResponse - r.body.Len()
	if len(p) > remaining {
		r.body.Write(p[:max(remaining, 0)])
		r.overflow = true
		return len(p), nil
	}
	r.body.Write(p)
	return len(p), nil
}

// decisionLogLine formats the single info line written for each decision.
func decisionLogLine(selectorID string, outcome decisionOutcome, elapsed time.Duration) string {
	return fmt.Sprintf("selector: decision id=%s label=%s confidence=%.3f target=%s latency=%dms cache_hit=%t reason=%s",
		selectorID, outcome.label, outcome.confidence, outcome.target, elapsed.Milliseconds(), outcome.cacheHit, outcome.reason)
}

// routeDecision routes one request on a decision selector and forwards it.
// The classification body is read and restored before the model is rewritten,
// so the forwarded request is byte-for-byte the client body except for model.
func (s *Server) routeDecision(w http.ResponseWriter, r *http.Request, next http.Handler, model string, selector config.SelectorConfig) {
	started := time.Now()

	text := ""
	if r.Method != http.MethodGet && strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			swaputil.SendResponse(w, r, http.StatusBadRequest, "could not read request body")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		if t, err := classificationText(r.URL.Path, body); err == nil {
			text = t
		}
	}

	outcome := s.decide(r.Context(), model, selector, text)
	if outcome.target == "" {
		// Only reachable with a config that bypassed validation.
		swaputil.SendResponse(w, r, http.StatusServiceUnavailable, "decision selector has no default target")
		return
	}

	updated, err := swaputil.ReplaceRequestModel(r, model, outcome.target)
	if err != nil {
		swaputil.SendResponse(w, r, http.StatusBadRequest, err.Error())
		return
	}

	w.Header().Set(headerRoutedModel, outcome.target)
	w.Header().Set(headerRoutedLabel, outcome.label)
	if outcome.reason != "" {
		w.Header().Set(headerRoutedReason, outcome.reason)
	}
	s.logs.ProxyLogs.Infof("%s", decisionLogLine(model, outcome, time.Since(started)))
	next.ServeHTTP(w, withDecisionContext(withSelectorContext(updated, model), outcome.label))
}
