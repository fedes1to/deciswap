package config

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	SelectorStrategyWarm      = "warm"
	SelectorStrategyPin       = "pin"
	SelectorStrategySpillover = "spillover"
	SelectorStrategyDecision  = "decision"
)

// Decision selector session modes. "request" is the original stateless
// behaviour; "sticky" decides once per session and pins the target;
// "adaptive" re-decides each turn but keeps the current target unless the new
// classification is materially different.
const (
	SelectorModeRequest  = "request"
	SelectorModeSticky   = "sticky"
	SelectorModeAdaptive = "adaptive"
)

// DefaultDecisionTimeout is the decider call deadline used when a decision
// selector does not set settings.timeout.
const DefaultDecisionTimeout = 5 * time.Second

// Defaults for session-aware decision selectors.
const (
	DefaultSessionHeader    = "X-Session-ID"
	DefaultSessionTTL       = time.Hour
	DefaultRefreshHeader    = "X-Route-Refresh"
	DefaultSwitchMargin     = 0.25
	DefaultSwitchConfidence = 0.60
	DefaultMinDwell         = 2 * time.Minute
)

// SelectorSettings contains strategy-specific selector settings.
type SelectorSettings struct {
	Spillover     int           `yaml:"spillover" json:"spillover"`
	Decider       string        `yaml:"decider" json:"decider"`
	Default       string        `yaml:"default" json:"default"`
	MinConfidence float64       `yaml:"minConfidence" json:"minConfidence"`
	Timeout       time.Duration `yaml:"timeout" json:"timeout"`
	Instructions  string        `yaml:"instructions" json:"instructions"`

	// Session-aware decision routing. Mode is request (default), sticky or
	// adaptive. The header names are matched case-insensitively by net/http.
	Mode             string        `yaml:"mode,omitempty" json:"mode,omitempty"`
	SessionHeader    string        `yaml:"sessionHeader,omitempty" json:"sessionHeader,omitempty"`
	SessionTTL       time.Duration `yaml:"sessionTTL,omitempty" json:"sessionTTL,omitempty"`
	RefreshHeader    string        `yaml:"refreshHeader,omitempty" json:"refreshHeader,omitempty"`
	UnloadDecider    bool          `yaml:"unloadDecider,omitempty" json:"unloadDecider,omitempty"`
	SwitchMargin     float64       `yaml:"switchMargin,omitempty" json:"switchMargin,omitempty"`
	SwitchConfidence float64       `yaml:"switchConfidence,omitempty" json:"switchConfidence,omitempty"`
	MinDwell         time.Duration `yaml:"minDwell,omitempty" json:"minDwell,omitempty"`

	// Context overrides the context length advertised for a selector. Zero
	// means "derive it": the smallest context among the selector's targets.
	Context int `yaml:"context,omitempty" json:"context,omitempty"`
}

// SelectorRoute is one choice a decision selector can make. The label is the
// map key in SelectorConfig.Routes.
type SelectorRoute struct {
	Model       string `yaml:"model" json:"model"`
	Description string `yaml:"description" json:"description"`
}

// SelectorConfig describes a virtual model ID that resolves to a concrete
// local model, alias, or peer model for each request.
//
// For strategy decision, Routes and Settings define the choices and the
// decider. Targets is derived during validation from the route models and the
// default model and is never read from user config.
type SelectorConfig struct {
	Strategy    string                   `yaml:"strategy" json:"strategy"`
	Targets     []string                 `yaml:"targets,omitempty" json:"targets"`
	Routes      map[string]SelectorRoute `yaml:"routes,omitempty" json:"routes,omitempty"`
	Settings    SelectorSettings         `yaml:"settings" json:"settings"`
	Name        string                   `yaml:"name" json:"name"`
	Description string                   `yaml:"description" json:"description"`
	Unlisted    bool                     `yaml:"unlisted" json:"unlisted"`
	Metadata    map[string]any           `yaml:"metadata" json:"metadata"`
}

// UnmarshalYAML applies selector defaults while retaining the distinction
// between an omitted spillover value and an explicitly invalid zero.
func (c *SelectorConfig) UnmarshalYAML(value *yaml.Node) error {
	type rawSelectorConfig SelectorConfig
	defaults := rawSelectorConfig{
		Targets: []string{},
		Settings: SelectorSettings{
			Spillover:        1,
			Timeout:          DefaultDecisionTimeout,
			Mode:             SelectorModeRequest,
			SessionHeader:    DefaultSessionHeader,
			SessionTTL:       DefaultSessionTTL,
			RefreshHeader:    DefaultRefreshHeader,
			SwitchMargin:     DefaultSwitchMargin,
			SwitchConfidence: DefaultSwitchConfidence,
			MinDwell:         DefaultMinDwell,
		},
		Metadata: map[string]any{},
	}
	if err := value.Decode(&defaults); err != nil {
		return err
	}
	*c = SelectorConfig(defaults)
	return nil
}

// MarshalYAML omits the derived targets of decision selectors so that a
// rendered config can be loaded again without tripping the "use routes" rule.
func (c SelectorConfig) MarshalYAML() (any, error) {
	type rawSelectorConfig SelectorConfig
	raw := rawSelectorConfig(c)
	if c.Strategy == SelectorStrategyDecision {
		raw.Targets = nil
	}
	return raw, nil
}

func validateSelectors(config Config) error {
	for selectorID, selector := range config.Selectors {
		if strings.TrimSpace(selectorID) == "" {
			return fmt.Errorf("selectors: selector names cannot be empty")
		}
		if _, found := config.Models[selectorID]; found {
			return fmt.Errorf("selectors.%s: name conflicts with model ID %q", selectorID, selectorID)
		}
		if _, found := config.aliases[selectorID]; found {
			return fmt.Errorf("selectors.%s: name conflicts with model alias %q", selectorID, selectorID)
		}

		switch selector.Strategy {
		case SelectorStrategyPin, SelectorStrategyWarm, SelectorStrategySpillover:
		case SelectorStrategyDecision:
			derived, err := validateDecisionSelector(config, selectorID, selector)
			if err != nil {
				return err
			}
			config.Selectors[selectorID] = derived
			continue
		case "":
			return fmt.Errorf("selectors.%s.strategy is required", selectorID)
		default:
			return fmt.Errorf("selectors.%s.strategy: unknown strategy %q (valid: warm, pin, spillover, decision)", selectorID, selector.Strategy)
		}
		if len(selector.Routes) > 0 {
			return fmt.Errorf("selectors.%s.routes is only valid for strategy %q", selectorID, SelectorStrategyDecision)
		}
		if len(selector.Targets) == 0 {
			return fmt.Errorf("selectors.%s.targets must contain at least one entry", selectorID)
		}

		resolvedTargets := make([]string, 0, len(selector.Targets))
		localTargets := make([]string, 0, len(selector.Targets))
		for i, target := range selector.Targets {
			if _, found := config.Selectors[target]; found {
				return fmt.Errorf("selectors.%s.targets[%d] references selector %q; selector chaining is not supported", selectorID, i, target)
			}
			if _, found := config.ResolveBaseModel(target); !found {
				return fmt.Errorf("selectors.%s.targets[%d] references unknown model %q", selectorID, i, target)
			}

			realName, local := config.RealModelName(target)
			if selector.Strategy == SelectorStrategyWarm {
				if !local {
					return fmt.Errorf("selectors.%s.targets[%d] must resolve to a local model for strategy %q", selectorID, i, selector.Strategy)
				}
			}
			if selector.Strategy == SelectorStrategySpillover {
				if local {
					localTargets = append(localTargets, realName)
				} else {
					peerID, modelID, _ := config.ResolvePeerModel(target)
					realName = PeerModelFQN(peerID, modelID)
				}
				resolvedTargets = append(resolvedTargets, realName)
			}
		}

		if selector.Strategy != SelectorStrategySpillover {
			continue
		}
		if selector.Settings.Spillover < 1 {
			return fmt.Errorf("selectors.%s.settings.spillover must be >= 1", selectorID)
		}

		seen := make(map[string]struct{}, len(resolvedTargets))
		for _, target := range resolvedTargets {
			if _, duplicate := seen[target]; duplicate {
				return fmt.Errorf("selectors.%s.targets contains duplicate resolved model %q", selectorID, target)
			}
			seen[target] = struct{}{}
		}
		if err := validateSpilloverCoexistence(config, selectorID, localTargets); err != nil {
			return err
		}
	}
	return nil
}

// validateDecisionSelector checks a decision selector and returns a copy with
// Targets set to the sorted, deduplicated route models plus the default.
func validateDecisionSelector(config Config, selectorID string, selector SelectorConfig) (SelectorConfig, error) {
	if len(selector.Targets) > 0 {
		return SelectorConfig{}, fmt.Errorf("selectors.%s.targets is not supported for strategy %q; list the choices in routes", selectorID, SelectorStrategyDecision)
	}
	if len(selector.Routes) == 0 {
		return SelectorConfig{}, fmt.Errorf("selectors.%s.routes must contain at least one entry", selectorID)
	}

	models := make(map[string]struct{}, len(selector.Routes)+1)
	for label, route := range selector.Routes {
		if strings.TrimSpace(label) == "" {
			return SelectorConfig{}, fmt.Errorf("selectors.%s.routes: label cannot be empty", selectorID)
		}
		if err := validateDecisionModel(config, selectorID, "routes."+label+".model", route.Model); err != nil {
			return SelectorConfig{}, err
		}
		models[route.Model] = struct{}{}
	}

	settings := selector.Settings
	if err := validateDecisionModel(config, selectorID, "settings.default", settings.Default); err != nil {
		return SelectorConfig{}, err
	}
	models[settings.Default] = struct{}{}

	if err := validateDecisionModel(config, selectorID, "settings.decider", settings.Decider); err != nil {
		return SelectorConfig{}, err
	}
	if settings.MinConfidence < 0 || settings.MinConfidence > 1 {
		return SelectorConfig{}, fmt.Errorf("selectors.%s.settings.minConfidence must be between 0 and 1, got %v", selectorID, settings.MinConfidence)
	}
	if settings.Timeout <= 0 {
		return SelectorConfig{}, fmt.Errorf("selectors.%s.settings.timeout must be greater than 0", selectorID)
	}

	switch settings.Mode {
	case "", SelectorModeRequest:
		settings.Mode = SelectorModeRequest
	case SelectorModeSticky, SelectorModeAdaptive:
	default:
		return SelectorConfig{}, fmt.Errorf("selectors.%s.settings.mode: unknown mode %q (valid: request, sticky, adaptive)", selectorID, settings.Mode)
	}
	if settings.SessionHeader == "" {
		settings.SessionHeader = DefaultSessionHeader
	}
	if settings.RefreshHeader == "" {
		settings.RefreshHeader = DefaultRefreshHeader
	}
	if settings.SessionTTL <= 0 {
		return SelectorConfig{}, fmt.Errorf("selectors.%s.settings.sessionTTL must be greater than 0", selectorID)
	}
	if settings.SwitchMargin < 0 || settings.SwitchMargin > 1 {
		return SelectorConfig{}, fmt.Errorf("selectors.%s.settings.switchMargin must be between 0 and 1, got %v", selectorID, settings.SwitchMargin)
	}
	if settings.SwitchConfidence < 0 || settings.SwitchConfidence > 1 {
		return SelectorConfig{}, fmt.Errorf("selectors.%s.settings.switchConfidence must be between 0 and 1, got %v", selectorID, settings.SwitchConfidence)
	}
	if settings.MinDwell < 0 {
		return SelectorConfig{}, fmt.Errorf("selectors.%s.settings.minDwell must be >= 0", selectorID)
	}
	if settings.Context < 0 {
		return SelectorConfig{}, fmt.Errorf("selectors.%s.settings.context must be >= 0", selectorID)
	}
	if settings.UnloadDecider && settings.Mode != SelectorModeSticky {
		return SelectorConfig{}, fmt.Errorf("selectors.%s.settings.unloadDecider is only valid with mode %q", selectorID, SelectorModeSticky)
	}
	selector.Settings = settings

	targets := make([]string, 0, len(models))
	for model := range models {
		targets = append(targets, model)
	}
	sort.Strings(targets)
	selector.Targets = targets
	return selector, nil
}

func validateDecisionModel(config Config, selectorID, field, model string) error {
	if strings.TrimSpace(model) == "" {
		return fmt.Errorf("selectors.%s.%s is required for strategy %q", selectorID, field, SelectorStrategyDecision)
	}
	if _, found := config.Selectors[model]; found {
		return fmt.Errorf("selectors.%s.%s references selector %q; selector chaining is not supported", selectorID, field, model)
	}
	if _, found := config.ResolveBaseModel(model); !found {
		return fmt.Errorf("selectors.%s.%s references unknown model %q", selectorID, field, model)
	}
	return nil
}

// validateSpilloverCoexistence ensures local spillover targets can remain
// resident together under the selected router's constraints.
func validateSpilloverCoexistence(config Config, selectorID string, targets []string) error {
	if len(targets) <= 1 {
		return nil
	}
	if config.Routing.Router.Use == "capacity" {
		capacity := config.Routing.Router.Settings.Capacity
		for pool, total := range capacity.Pools {
			var used int64
			for _, target := range targets {
				used += config.Models[target].Capacities[pool]
			}
			if used > total {
				return fmt.Errorf("selectors.%s spillover targets exceed capacity total in pool %q", selectorID, pool)
			}
		}
		return nil
	}

	if config.Routing.Router.Use == "matrix" {
		matrix := config.Routing.Router.Settings.Matrix
		if matrix != nil && matrix.program != nil && matrix.program.CanContainAll(targets) {
			return nil
		}
		return fmt.Errorf("selectors.%s.targets must all appear together in one expanded matrix set", selectorID)
	}

	groupOf := make(map[string]string, len(config.Models))
	for groupID, group := range config.Routing.Router.Settings.Groups {
		for _, member := range group.Members {
			groupOf[member] = groupID
		}
	}

	groupID := groupOf[targets[0]]
	if groupID == "" {
		return fmt.Errorf("selectors.%s target %q is not in a routing group", selectorID, targets[0])
	}
	if config.Routing.Router.Settings.Groups[groupID].Swap {
		return fmt.Errorf("selectors.%s spillover targets must share a group with swap: false", selectorID)
	}
	for _, target := range targets[1:] {
		if groupOf[target] != groupID {
			return fmt.Errorf("selectors.%s spillover targets must share one routing group", selectorID)
		}
	}
	return nil
}
