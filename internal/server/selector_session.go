package server

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/mostlygeek/llama-swap/internal/config"
)

const (
	// sessionStoreMax bounds the number of session state entries. Each entry is
	// a few hundred bytes, so this is a few megabytes at worst.
	sessionStoreMax = 4096

	// headerRoutedSession reports how the session resolved this request:
	// "decided" (first decision), "pinned" (reused), "switched" (adaptive
	// changed target) or "unchanged" (adaptive kept the target).
	headerRoutedSession = "X-Routed-Session"

	// unloadGrace lets an in-flight decision finish before the sticky mode
	// stops the decider process.
	unloadGrace = 500 * time.Millisecond
)

// selectorSessionKey identifies one conversation for one selector. Two
// selectors may see the same client session header without sharing state.
type selectorSessionKey struct {
	selector string
	session  string
}

// selectorSessionEntry is the routing state pinned to a session. The zero
// target with forceNext set is a tombstone written by the refresh endpoint:
// the next request must ask the decider again even if the text is cached.
type selectorSessionEntry struct {
	key           selectorSessionKey
	target        string
	label         string
	confidence    float64
	probabilities map[string]float64
	turns         int
	pinnedAt      time.Time
	lastSwitch    time.Time
	lastSeen      time.Time
	expires       time.Time
	forceNext     bool
	elem          *list.Element
}

func cloneProbabilities(in map[string]float64) map[string]float64 {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]float64, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// selectorSessionStore is a bounded LRU of session routing state, with an
// idle TTL per entry. It is safe for concurrent use.
type selectorSessionStore struct {
	mu      sync.Mutex
	max     int
	entries map[selectorSessionKey]*selectorSessionEntry
	order   *list.List
	now     func() time.Time
}

func newSelectorSessionStore(max int) *selectorSessionStore {
	return &selectorSessionStore{
		max:     max,
		entries: make(map[selectorSessionKey]*selectorSessionEntry, max),
		order:   list.New(),
		now:     time.Now,
	}
}

// get returns a copy of the live entry for key. Expired entries are removed.
func (s *selectorSessionStore) get(key selectorSessionKey) (selectorSessionEntry, bool) {
	if s == nil {
		return selectorSessionEntry{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[key]
	if !ok {
		return selectorSessionEntry{}, false
	}
	if !s.now().Before(entry.expires) {
		s.remove(entry)
		return selectorSessionEntry{}, false
	}
	s.order.MoveToFront(entry.elem)
	return *entry, true
}

// put inserts or replaces an entry and returns the stored copy.
func (s *selectorSessionStore) put(entry *selectorSessionEntry) {
	if s == nil || entry == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry.probabilities = cloneProbabilities(entry.probabilities)
	if existing, ok := s.entries[entry.key]; ok {
		entry.elem = existing.elem
		*s.entries[entry.key] = *entry
		s.order.MoveToFront(entry.elem)
		return
	}
	entry.elem = s.order.PushFront(entry)
	s.entries[entry.key] = entry
	for len(s.entries) > s.max {
		s.remove(s.order.Back().Value.(*selectorSessionEntry))
	}
}

// touch extends an entry's idle TTL.
func (s *selectorSessionStore) touch(key selectorSessionKey, ttl time.Duration) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[key]
	if !ok {
		return
	}
	now := s.now()
	entry.lastSeen = now
	entry.expires = now.Add(ttl)
	s.order.MoveToFront(entry.elem)
}

func (s *selectorSessionStore) delete(key selectorSessionKey) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry, ok := s.entries[key]; ok {
		s.remove(entry)
	}
}

// deleteSelector removes every session for one selector and returns the count.
func (s *selectorSessionStore) deleteSelector(selector string) int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for key, entry := range s.entries {
		if key.selector == selector {
			s.remove(entry)
			n++
		}
	}
	return n
}

// refresh marks a session for one forced re-decision. With an empty session
// every session of the selector is dropped.
func (s *selectorSessionStore) refresh(selector, session string, ttl time.Duration) int {
	if s == nil {
		return 0
	}
	if session == "" {
		return s.deleteSelector(selector)
	}
	key := selectorSessionKey{selector: selector, session: session}
	now := s.now()
	s.put(&selectorSessionEntry{
		key:       key,
		forceNext: true,
		pinnedAt:  now,
		lastSeen:  now,
		expires:   now.Add(ttl),
	})
	return 1
}

func (s *selectorSessionStore) remove(entry *selectorSessionEntry) {
	s.order.Remove(entry.elem)
	delete(s.entries, entry.key)
}

// deciderInflight counts in-flight System One calls per decider model so a
// sticky unload never stops a process another request is still using.
type deciderInflight struct {
	mu     sync.Mutex
	counts map[string]int
}

func newDeciderInflight() *deciderInflight {
	return &deciderInflight{counts: make(map[string]int)}
}

func (d *deciderInflight) begin(model string) {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.counts[model]++
	d.mu.Unlock()
}

func (d *deciderInflight) end(model string) {
	if d == nil {
		return
	}
	d.mu.Lock()
	if d.counts[model] > 0 {
		d.counts[model]--
	}
	d.mu.Unlock()
}

func (d *deciderInflight) count(model string) int {
	if d == nil {
		return 0
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.counts[model]
}

// requestSessionID reads the session key named by the selector settings.
func requestSessionID(r *http.Request, header string) string {
	if r == nil || header == "" {
		return ""
	}
	return strings.TrimSpace(r.Header.Get(header))
}

// refreshRequested reports whether the client asked for a fresh decision on
// this request. The value is deliberately forgiving so shell clients can send
// "1", "true", "yes" or "on".
func refreshRequested(r *http.Request, header string) bool {
	if r == nil || header == "" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(r.Header.Get(header))) {
	case "1", "true", "yes", "on", "refresh":
		return true
	}
	return false
}

// sessionLogID shortens a session key for logs. The full value can be an
// email address or a conversation ID, so logs only carry a stable hash.
func sessionLogID(session string) string {
	if session == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(session))
	return hex.EncodeToString(sum[:4])
}

func outcomeFromEntry(entry selectorSessionEntry) decisionOutcome {
	return decisionOutcome{
		target:        entry.target,
		label:         entry.label,
		confidence:    entry.confidence,
		probabilities: cloneProbabilities(entry.probabilities),
	}
}

func newSessionEntry(key selectorSessionKey, outcome decisionOutcome, ttl time.Duration, now time.Time) *selectorSessionEntry {
	return &selectorSessionEntry{
		key:           key,
		target:        outcome.target,
		label:         outcome.label,
		confidence:    outcome.confidence,
		probabilities: cloneProbabilities(outcome.probabilities),
		turns:         1,
		pinnedAt:      now,
		lastSwitch:    now,
		lastSeen:      now,
		expires:       now.Add(ttl),
	}
}

// chooseAdaptive keeps the session's current target unless the fresh
// classification is both confident and materially better than the label the
// session is already on, and the session has dwelled long enough to avoid
// thrashing on a single borderline turn.
func chooseAdaptive(entry selectorSessionEntry, fresh decisionOutcome, settings config.SelectorSettings, now time.Time) (decisionOutcome, string) {
	if entry.target == "" {
		return fresh, "decided"
	}
	if fresh.reason != "" {
		// A decider failure must never move a session; the current target is
		// known good.
		kept := outcomeFromEntry(entry)
		return kept, "unchanged"
	}
	if fresh.target == entry.target {
		return fresh, "pinned"
	}
	if now.Sub(entry.lastSwitch) < settings.MinDwell {
		kept := outcomeFromEntry(entry)
		return kept, "pinned"
	}
	if len(fresh.probabilities) > 0 {
		best := fresh.probabilities[fresh.label]
		current := fresh.probabilities[entry.label]
		if best < settings.SwitchConfidence || best-current < settings.SwitchMargin {
			kept := outcomeFromEntry(entry)
			return kept, "pinned"
		}
	} else if fresh.confidence < settings.SwitchConfidence {
		kept := outcomeFromEntry(entry)
		return kept, "pinned"
	}
	return fresh, "switched"
}

// resolveDecision applies the selector's session mode to one request. Without
// a session header (or with mode request) it is the original stateless
// decision. A sticky session decides once and then reuses the target until the
// client refreshes it or the session expires. An adaptive session re-decides
// every turn but only moves when chooseAdaptive says the change is material.
func (s *Server) resolveDecision(r *http.Request, selectorID string, selector config.SelectorConfig, text string) decisionOutcome {
	mode := selector.Settings.Mode
	if mode == "" {
		mode = config.SelectorModeRequest
	}
	if mode == config.SelectorModeRequest || s.sessions == nil {
		return s.decide(r.Context(), selectorID, selector, text, false)
	}

	session := requestSessionID(r, selector.Settings.SessionHeader)
	if session == "" {
		// No session key: behave exactly like the stateless selector so
		// clients that do not send the header keep working.
		return s.decide(r.Context(), selectorID, selector, text, false)
	}

	key := selectorSessionKey{selector: selectorID, session: session}
	force := refreshRequested(r, selector.Settings.RefreshHeader)
	entry, found := s.sessions.get(key)
	if found && entry.forceNext {
		// Refresh endpoint tombstone: force one re-decision and drop it.
		s.sessions.delete(key)
		found = false
		force = true
	}
	ttl := selector.Settings.SessionTTL
	now := s.sessions.now()

	switch mode {
	case config.SelectorModeSticky:
		if found && entry.target != "" && !force {
			s.sessions.touch(key, ttl)
			out := outcomeFromEntry(entry)
			out.state = "pinned"
			return out
		}
		out := s.decide(r.Context(), selectorID, selector, text, force)
		s.sessions.put(newSessionEntry(key, out, ttl, now))
		out.state = "decided"
		if selector.Settings.UnloadDecider {
			s.maybeUnloadDecider(selector.Settings.Decider, out.target)
		}
		return out

	case config.SelectorModeAdaptive:
		if !found || entry.target == "" || force {
			out := s.decide(r.Context(), selectorID, selector, text, force)
			s.sessions.put(newSessionEntry(key, out, ttl, now))
			out.state = "decided"
			return out
		}
		fresh := s.decide(r.Context(), selectorID, selector, text, false)
		out, state := chooseAdaptive(entry, fresh, selector.Settings, now)
		out.state = state
		next := entry
		next.target = out.target
		next.label = out.label
		next.confidence = out.confidence
		next.probabilities = cloneProbabilities(out.probabilities)
		if state == "switched" {
			next.lastSwitch = now
		}
		next.turns++
		next.lastSeen = now
		next.expires = now.Add(ttl)
		s.sessions.put(&next)
		return out
	}

	return s.decide(r.Context(), selectorID, selector, text, false)
}

// maybeUnloadDecider stops a local decider after a sticky decision. A short
// grace period lets concurrent decisions finish; if one is still running the
// request that owns it schedules its own unload when it completes.
func (s *Server) maybeUnloadDecider(decider, target string) {
	if decider == "" || decider == target || s.local == nil || !s.local.Handles(decider) {
		return
	}
	go func() {
		time.Sleep(unloadGrace)
		if s.deciderCalls.count(decider) != 0 {
			return
		}
		s.logs.ProxyLogs.Debugf("selector: unloading decision model %s after sticky decision", decider)
		s.local.Unload(0, decider)
	}()
}
