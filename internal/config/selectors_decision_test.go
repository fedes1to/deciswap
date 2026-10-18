package config

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const decisionBaseModels = `
models:
  fable:
    cmd: echo ${PORT}
  opus:
    cmd: echo ${PORT}
  kev:
    cmd: echo ${PORT}
`

func TestConfig_SelectorDecision_Validation(t *testing.T) {
	tests := []struct {
		name    string
		config  string
		wantErr string
	}{
		{
			name: "missing routes",
			config: `
selectors:
  auto:
    strategy: decision
    settings: { decider: kev, default: fable }
`,
			wantErr: "routes must contain at least one entry",
		},
		{
			name: "targets not allowed",
			config: `
selectors:
  auto:
    strategy: decision
    targets: [fable]
    routes:
      general: { model: fable }
    settings: { decider: kev, default: fable }
`,
			wantErr: "list the choices in routes",
		},
		{
			name: "routes not allowed on pin",
			config: `
selectors:
  public:
    strategy: pin
    targets: [fable]
    routes:
      general: { model: fable }
`,
			wantErr: "routes is only valid for strategy",
		},
		{
			name: "blank label",
			config: `
selectors:
  auto:
    strategy: decision
    routes:
      " ": { model: fable }
    settings: { decider: kev, default: fable }
`,
			wantErr: "label cannot be empty",
		},
		{
			name: "route model unknown",
			config: `
selectors:
  auto:
    strategy: decision
    routes:
      general: { model: missing }
    settings: { decider: kev, default: fable }
`,
			wantErr: "routes.general.model",
		},
		{
			name: "route references selector",
			config: `
selectors:
  auto:
    strategy: decision
    routes:
      general: { model: other }
    settings: { decider: kev, default: fable }
  other:
    strategy: pin
    targets: [fable]
`,
			wantErr: "routes.general.model",
		},
		{
			name: "default unknown",
			config: `
selectors:
  auto:
    strategy: decision
    routes:
      general: { model: fable }
    settings: { decider: kev, default: missing }
`,
			wantErr: "settings.default",
		},
		{
			name: "decider missing",
			config: `
selectors:
  auto:
    strategy: decision
    routes:
      general: { model: fable }
    settings: { default: fable }
`,
			wantErr: "settings.decider",
		},
		{
			name: "decider is a selector",
			config: `
selectors:
  auto:
    strategy: decision
    routes:
      general: { model: fable }
    settings: { decider: other, default: fable }
  other:
    strategy: pin
    targets: [kev]
`,
			wantErr: "settings.decider",
		},
		{
			name: "minConfidence above one",
			config: `
selectors:
  auto:
    strategy: decision
    routes:
      general: { model: fable }
    settings: { decider: kev, default: fable, minConfidence: 1.5 }
`,
			wantErr: "minConfidence must be between 0 and 1",
		},
		{
			name: "minConfidence negative",
			config: `
selectors:
  auto:
    strategy: decision
    routes:
      general: { model: fable }
    settings: { decider: kev, default: fable, minConfidence: -0.1 }
`,
			wantErr: "minConfidence must be between 0 and 1",
		},
		{
			name: "unknown mode",
			config: `
selectors:
  auto:
    strategy: decision
    routes:
      general: { model: fable }
    settings: { decider: kev, default: fable, mode: sometimes }
`,
			wantErr: "settings.mode",
		},
		{
			name: "sessionTTL zero",
			config: `
selectors:
  auto:
    strategy: decision
    routes:
      general: { model: fable }
    settings: { decider: kev, default: fable, mode: sticky, sessionTTL: 0s }
`,
			wantErr: "sessionTTL must be greater than 0",
		},
		{
			name: "switchMargin above one",
			config: `
selectors:
  auto:
    strategy: decision
    routes:
      general: { model: fable }
    settings: { decider: kev, default: fable, mode: adaptive, switchMargin: 1.5 }
`,
			wantErr: "switchMargin must be between 0 and 1",
		},
		{
			name: "unloadDecider only for sticky",
			config: `
selectors:
  auto:
    strategy: decision
    routes:
      general: { model: fable }
    settings: { decider: kev, default: fable, mode: adaptive, unloadDecider: true }
`,
			wantErr: "unloadDecider is only valid",
		},
		{
			name: "selector context negative",
			config: `
selectors:
  auto:
    strategy: decision
    routes:
      general: { model: fable }
    settings: { decider: kev, default: fable, context: -1 }
`,
			wantErr: "settings.context must be >= 0",
		},
		{
			name: "timeout zero",
			config: `
selectors:
  auto:
    strategy: decision
    routes:
      general: { model: fable }
    settings: { decider: kev, default: fable, timeout: 0s }
`,
			wantErr: "timeout must be greater than 0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoadConfigFromReader(strings.NewReader(decisionBaseModels + tt.config))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestConfig_SelectorDecision_DerivesTargetsAndDefaults(t *testing.T) {
	cfg, err := LoadConfigFromReader(strings.NewReader(decisionBaseModels + `
selectors:
  auto:
    strategy: decision
    routes:
      security: { model: opus, description: "exploits" }
      general: { model: fable, description: "writing" }
      coding: { model: opus, description: "code" }
    settings:
      decider: kev
      default: fable
`))
	require.NoError(t, err)

	auto := cfg.Selectors["auto"]
	assert.Equal(t, []string{"fable", "opus"}, auto.Targets, "targets are derived, sorted and deduplicated")
	assert.Equal(t, 5*time.Second, auto.Settings.Timeout, "timeout defaults to 5s")
	assert.Equal(t, "writing", auto.Routes["general"].Description)
	assert.Equal(t, SelectorModeRequest, auto.Settings.Mode)
	assert.Equal(t, DefaultSessionHeader, auto.Settings.SessionHeader)
	assert.Equal(t, DefaultSessionTTL, auto.Settings.SessionTTL)
	assert.Equal(t, DefaultRefreshHeader, auto.Settings.RefreshHeader)
	assert.Equal(t, DefaultSwitchMargin, auto.Settings.SwitchMargin)
	assert.Equal(t, DefaultSwitchConfidence, auto.Settings.SwitchConfidence)
	assert.Equal(t, DefaultMinDwell, auto.Settings.MinDwell)
}

func TestConfig_SelectorDecision_SessionModesAccepted(t *testing.T) {
	cfg, err := LoadConfigFromReader(strings.NewReader(decisionBaseModels + `
selectors:
  sticky:
    strategy: decision
    routes:
      general: { model: fable }
    settings:
      decider: kev
      default: fable
      mode: sticky
      unloadDecider: true
  adaptive:
    strategy: decision
    routes:
      general: { model: fable }
    settings:
      decider: kev
      default: fable
      mode: adaptive
      switchMargin: 0.3
      switchConfidence: 0.7
      minDwell: 90s
`))
	require.NoError(t, err)
	sticky := cfg.Selectors["sticky"]
	assert.Equal(t, SelectorModeSticky, sticky.Settings.Mode)
	assert.True(t, sticky.Settings.UnloadDecider)
	adaptive := cfg.Selectors["adaptive"]
	assert.Equal(t, SelectorModeAdaptive, adaptive.Settings.Mode)
	assert.Equal(t, 0.3, adaptive.Settings.SwitchMargin)
	assert.Equal(t, 90*time.Second, adaptive.Settings.MinDwell)
}

func TestConfig_SelectorDecision_ReloadRoundTrip(t *testing.T) {
	cfg, err := LoadConfigFromReader(strings.NewReader(decisionBaseModels + `
selectors:
  auto:
    strategy: decision
    routes:
      general: { model: fable }
    settings: { decider: kev, default: fable, minConfidence: 0.5 }
`))
	require.NoError(t, err)

	rendered, found, err := cfg.RedactedYAML("selectors")
	require.NoError(t, err)
	require.True(t, found)

	_, err = LoadConfigFromReader(strings.NewReader(decisionBaseModels + rendered))
	require.NoError(t, err, "rendered decision selector must load again without a targets error")
}
