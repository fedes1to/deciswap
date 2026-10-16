package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mostlygeek/llama-swap/internal/config"
)

func TestConfigureTailcatListener_RequiresModels(t *testing.T) {
	cfg := config.Config{}
	err := configureTailcatListener(&cfg, "/path/to/server.private.json")
	require.ErrorContains(t, err, "tailcat.models")

	cfg.Tailcat = &config.TailcatConfig{}
	err = configureTailcatListener(&cfg, "/path/to/server.private.json")
	require.ErrorContains(t, err, "tailcat.models")

	cfg.Tailcat.Models = []string{"model"}
	require.NoError(t, configureTailcatListener(&cfg, "/path/to/server.private.json"))
	assert.True(t, cfg.TailcatEnabled())
}

func TestConfigureTailcatListener_DisabledWithoutFlag(t *testing.T) {
	cfg := config.Config{}
	require.NoError(t, configureTailcatListener(&cfg, ""))
	assert.False(t, cfg.TailcatEnabled())
}

func TestLoadTailcatListenerKey_InvalidFile(t *testing.T) {
	_, err := loadTailcatListenerKey(filepath.Join(t.TempDir(), "missing.private.json"), true)
	require.ErrorContains(t, err, "-listen-tailcat")
}

func TestRunValidate_ValidConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yml")
	require.NoError(t, os.WriteFile(configPath, []byte(`
models:
  model1:
    cmd: server --port ${PORT}
    proxy: http://localhost:8080
`), 0644))

	var buf strings.Builder
	code := runValidate(configPath, "", &buf)

	assert.Equal(t, 0, code)
	assert.NotEmpty(t, buf.String())
	assert.Contains(t, buf.String(), "config is valid")
}

func TestRunValidate_BrokenConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yml")
	require.NoError(t, os.WriteFile(configPath, []byte(`
models:
  model1:
    cmd: server --port ${PORT} ${UNKNOWN_MACRO}
    proxy: http://localhost:8080
`), 0644))

	var buf strings.Builder
	code := runValidate(configPath, "", &buf)

	assert.Equal(t, 1, code)
	assert.NotEmpty(t, buf.String())
	assert.Contains(t, buf.String(), "failed")
}

func TestRunValidate_ConfigDir(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.yml"), []byte(`
models:
  model1:
    cmd: server --port ${PORT}
    proxy: http://localhost:8080
`), 0644))

	var buf strings.Builder
	code := runValidate("", dir, &buf)

	assert.Equal(t, 0, code)
	assert.NotEmpty(t, buf.String())
	assert.Contains(t, buf.String(), "config is valid")
}

func TestReload_SourcesFingerprint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	write := func(content string) {
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}
	write("healthCheckTimeout: 30\n")
	base, err := sourcesFingerprint(path, "")
	require.NoError(t, err)

	// The watcher reports a write whose bytes were already loaded: the same
	// content with a newer mtime must not look like a config change.
	write("healthCheckTimeout: 30\n")
	later := time.Now().Add(time.Hour)
	require.NoError(t, os.Chtimes(path, later, later))
	same, err := sourcesFingerprint(path, "")
	require.NoError(t, err)
	assert.Equal(t, base, same)

	write("healthCheckTimeout: 31\n")
	changed, err := sourcesFingerprint(path, "")
	require.NoError(t, err)
	assert.NotEqual(t, base, changed)
}

func TestReload_LoadSourcesFingerprint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("models:\n  alpha:\n    cmd: echo\n    proxy: http://localhost:9999\n"), 0o644))

	_, fp, err := loadSources(path, "")
	require.NoError(t, err)
	want, err := sourcesFingerprint(path, "")
	require.NoError(t, err)
	assert.Equal(t, want, fp)

	// A load that fails must not record a fingerprint, so a later watcher call
	// reloads once the file is fixed.
	require.NoError(t, os.WriteFile(path, []byte("models: [\n"), 0o644))
	_, fp, err = loadSources(path, "")
	require.Error(t, err)
	assert.Empty(t, fp)
}
