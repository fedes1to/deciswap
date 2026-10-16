package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mostlygeek/llama-swap/internal/auth"
	"github.com/mostlygeek/llama-swap/internal/config"
	"github.com/mostlygeek/llama-swap/internal/store/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const adminTestPassword = "correct-horse-battery"

const adminTestConfig = `models:
  m1:
    cmd: svr --port ${PORT}
`

type adminTestEnv struct {
	handler   http.Handler
	auth      *auth.Service
	cfgPath   string
	reloads   *atomic.Int32
	setupTok  string
	configDir string
}

func newAdminTestEnv(t *testing.T) *adminTestEnv {
	t.Helper()
	st, err := sqlite.New("")
	require.NoError(t, err)
	t.Cleanup(func() { st.Close() })

	svc, err := auth.New(context.Background(), st.Auth(), auth.Options{})
	require.NoError(t, err)
	token, ok := svc.SetupToken()
	require.True(t, ok)

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(adminTestConfig), 0o644))

	reloads := &atomic.Int32{}
	cfg := config.Config{Models: map[string]config.ModelConfig{"m1": {Cmd: "svr --port ${PORT}"}}}
	deps := AdminDeps{
		Auth:       svc,
		ConfigPath: cfgPath,
		Reload:     func() { reloads.Add(1) },
	}
	return &adminTestEnv{
		handler:   newAdminHandler(deps, cfg),
		auth:      svc,
		cfgPath:   cfgPath,
		reloads:   reloads,
		setupTok:  token,
		configDir: dir,
	}
}

func (e *adminTestEnv) do(t *testing.T, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		require.NoError(t, json.NewEncoder(&buf).Encode(body))
	}
	req := httptest.NewRequest(method, path, &buf)
	req.RemoteAddr = "192.0.2.1:4000"
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

func (e *adminTestEnv) setUp(t *testing.T) {
	t.Helper()
	rec := e.do(t, http.MethodPost, "/api/admin/setup", map[string]string{
		"token": e.setupTok, "username": "admin", "password": adminTestPassword,
	})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
}

func (e *adminTestEnv) configFile(t *testing.T, path string) adminConfigFileJSON {
	t.Helper()
	rec := e.do(t, http.MethodGet, "/api/admin/config", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp struct {
		Files []adminConfigFileJSON `json:"files"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	for _, f := range resp.Files {
		if f.Path == path {
			return f
		}
	}
	t.Fatalf("config file %s not listed", path)
	return adminConfigFileJSON{}
}

func TestAdmin_SetupTokenSingleUse(t *testing.T) {
	env := newAdminTestEnv(t)

	rec := env.do(t, http.MethodPost, "/api/admin/setup", map[string]string{
		"token": env.setupTok, "username": "admin", "password": adminTestPassword,
	})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	require.NotEmpty(t, rec.Result().Cookies())
	assert.Equal(t, auth.SessionCookieName, rec.Result().Cookies()[0].Name)

	rec = env.do(t, http.MethodPost, "/api/admin/setup", map[string]string{
		"token": env.setupTok, "username": "admin", "password": adminTestPassword,
	})
	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
}

func TestAdmin_SetupErrors(t *testing.T) {
	tests := []struct {
		name   string
		token  string
		status int
	}{
		{name: "bad token", token: "wrong", status: http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newAdminTestEnv(t)
			rec := env.do(t, http.MethodPost, "/api/admin/setup", map[string]string{
				"token": tt.token, "username": "admin", "password": adminTestPassword,
			})
			assert.Equal(t, tt.status, rec.Code, rec.Body.String())
		})
	}
}

func TestAdmin_SetupRejectsShortPassword(t *testing.T) {
	env := newAdminTestEnv(t)
	rec := env.do(t, http.MethodPost, "/api/admin/setup", map[string]string{
		"token": env.setupTok, "username": "admin", "password": "short",
	})
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

func TestAdmin_LoginWithoutTwoFactor(t *testing.T) {
	env := newAdminTestEnv(t)
	env.setUp(t)

	rec := env.do(t, http.MethodPost, "/api/admin/login", map[string]string{
		"username": "admin", "password": adminTestPassword,
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotEmpty(t, rec.Result().Cookies())
	assert.Equal(t, auth.SessionCookieName, rec.Result().Cookies()[0].Name)

	rec = env.do(t, http.MethodPost, "/api/admin/login", map[string]string{
		"username": "admin", "password": "not-the-password",
	})
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.NotContains(t, rec.Body.String(), "totpRequired")
}

func TestAdmin_ConfigPutRejectsStaleHash(t *testing.T) {
	env := newAdminTestEnv(t)
	cur := env.configFile(t, env.cfgPath)

	rec := env.do(t, http.MethodPut, "/api/admin/config", map[string]string{
		"path": env.cfgPath, "content": adminTestConfig + "# edited\n", "expectedHash": strings.Repeat("0", 64),
	})
	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "file changed on disk; reload and merge")

	got, err := os.ReadFile(env.cfgPath)
	require.NoError(t, err)
	assert.Equal(t, cur.Content, string(got))
	assert.Equal(t, int32(0), env.reloads.Load())
}

func TestAdmin_ConfigPutRejectsUnknownPath(t *testing.T) {
	env := newAdminTestEnv(t)
	other := filepath.Join(env.configDir, "other.yaml")
	require.NoError(t, os.WriteFile(other, []byte(adminTestConfig), 0o644))

	rec := env.do(t, http.MethodPut, "/api/admin/config", map[string]string{
		"path": other, "content": adminTestConfig, "expectedHash": strings.Repeat("0", 64),
	})
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())

	got, err := os.ReadFile(other)
	require.NoError(t, err)
	assert.Equal(t, adminTestConfig, string(got))
	assert.Equal(t, int32(0), env.reloads.Load())
}

func TestAdmin_ConfigPutWritesAtomicallyWithBackup(t *testing.T) {
	env := newAdminTestEnv(t)
	cur := env.configFile(t, env.cfgPath)
	next := adminTestConfig + "logLevel: debug\n"

	rec := env.do(t, http.MethodPut, "/api/admin/config", map[string]string{
		"path": env.cfgPath, "content": next, "expectedHash": cur.Hash,
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp struct {
		Hash      string `json:"hash"`
		Reloading bool   `json:"reloading"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.True(t, resp.Reloading)
	assert.Equal(t, adminContentHash([]byte(next)), resp.Hash)

	got, err := os.ReadFile(env.cfgPath)
	require.NoError(t, err)
	assert.Equal(t, next, string(got))

	bak, err := os.ReadFile(env.cfgPath + ".bak")
	require.NoError(t, err)
	assert.Equal(t, adminTestConfig, string(bak))

	assert.Eventually(t, func() bool { return env.reloads.Load() == 1 }, time.Second, 5*time.Millisecond)
	assert.Equal(t, int32(1), env.reloads.Load())
}

func TestAdmin_ConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		content string
		status  int
	}{
		{name: "bad yaml", content: "models: [\n", status: http.StatusBadRequest},
		{name: "valid", content: adminTestConfig, status: http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newAdminTestEnv(t)
			rec := env.do(t, http.MethodPost, "/api/admin/config/validate", map[string]string{
				"path": env.cfgPath, "content": tt.content,
			})
			assert.Equal(t, tt.status, rec.Code, rec.Body.String())

			got, err := os.ReadFile(env.cfgPath)
			require.NoError(t, err)
			assert.Equal(t, adminTestConfig, string(got))
			assert.Equal(t, int32(0), env.reloads.Load())
		})
	}
}

func TestAdmin_KeyCreateReturnsPlaintextOnce(t *testing.T) {
	env := newAdminTestEnv(t)

	rec := env.do(t, http.MethodPost, "/api/admin/keys", map[string]any{
		"name": "ci", "models": []string{"m1"},
	})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var created struct {
		Key string `json:"key"`
		ID  string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))
	require.True(t, strings.HasPrefix(created.Key, auth.KeyPrefix))

	rec = env.do(t, http.MethodGet, "/api/admin/keys", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), created.Key)
	assert.NotContains(t, rec.Body.String(), `"key"`)
	assert.Contains(t, rec.Body.String(), created.ID)
}

func TestAdmin_KeyCreateRejectsInvalidModels(t *testing.T) {
	tests := []struct {
		name   string
		models []string
	}{
		{name: "empty", models: []string{}},
		{name: "unknown", models: []string{"nope"}},
		{name: "star mixed", models: []string{"*", "m1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newAdminTestEnv(t)
			rec := env.do(t, http.MethodPost, "/api/admin/keys", map[string]any{
				"name": "ci", "models": tt.models,
			})
			assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		})
	}
}

func TestAdmin_KeyCreateAcceptsStarAlone(t *testing.T) {
	env := newAdminTestEnv(t)
	rec := env.do(t, http.MethodPost, "/api/admin/keys", map[string]any{
		"name": "all", "models": []string{"*"},
	})
	assert.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
}

func TestAdmin_UnknownJSONFieldRejected(t *testing.T) {
	env := newAdminTestEnv(t)
	rec := env.do(t, http.MethodPost, "/api/admin/keys", map[string]any{
		"name": "ci", "models": []string{"m1"}, "extra": true,
	})
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}
