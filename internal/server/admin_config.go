package server

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"

	"github.com/mostlygeek/llama-swap/internal/config"
)

type adminConfigFileJSON struct {
	Path    string `json:"path"`
	Kind    string `json:"kind"`
	Hash    string `json:"hash"`
	Content string `json:"content"`
}

// adminConfigSources returns the absolute paths of every loaded config source.
func (h *adminHandler) adminConfigSources() ([]string, error) {
	return config.ConfigSourceFiles(h.deps.ConfigPath, h.deps.ConfigDir)
}

func adminContentHash(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func (h *adminHandler) listConfig(w http.ResponseWriter, r *http.Request) {
	sources, err := h.adminConfigSources()
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var configAbs string
	if h.deps.ConfigPath != "" {
		abs, err := filepath.Abs(h.deps.ConfigPath)
		if err != nil {
			writeAdminInternal(w)
			return
		}
		configAbs = abs
	}
	files := make([]adminConfigFileJSON, 0, len(sources))
	for _, path := range sources {
		content, err := os.ReadFile(path)
		if err != nil {
			writeAdminError(w, http.StatusInternalServerError, fmt.Sprintf("reading %s: %v", path, err))
			return
		}
		kind := "dir"
		if path == configAbs {
			kind = "config"
		}
		files = append(files, adminConfigFileJSON{
			Path:    path,
			Kind:    kind,
			Hash:    adminContentHash(content),
			Content: string(content),
		})
	}
	writeAdminJSON(w, http.StatusOK, map[string][]adminConfigFileJSON{"files": files})
}

// checkConfigSourcePath answers 400 and returns false unless path is one of
// the loaded config sources.
func (h *adminHandler) checkConfigSourcePath(w http.ResponseWriter, path string) bool {
	sources, err := h.adminConfigSources()
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, err.Error())
		return false
	}
	if !slices.Contains(sources, path) {
		writeAdminError(w, http.StatusBadRequest, "path is not a loaded config source")
		return false
	}
	return true
}

func (h *adminHandler) validateConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if !decodeAdminBody(w, r, adminBodyLimit, &req) {
		return
	}
	if !h.checkConfigSourcePath(w, req.Path) {
		return
	}
	cfg, err := config.LoadConfigSourcesWithOverlay(h.deps.ConfigPath, h.deps.ConfigDir, req.Path, []byte(req.Content))
	if err != nil {
		writeAdminError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeAdminJSON(w, http.StatusOK, map[string]any{
		"valid":  true,
		"models": len(cfg.Models),
		"peers":  len(cfg.Peers),
	})
}

func (h *adminHandler) saveConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path         string `json:"path"`
		Content      string `json:"content"`
		ExpectedHash string `json:"expectedHash"`
	}
	if !decodeAdminBody(w, r, adminConfigBodyLimit, &req) {
		return
	}

	h.configMu.Lock()
	defer h.configMu.Unlock()

	if !h.checkConfigSourcePath(w, req.Path) {
		return
	}
	current, err := os.ReadFile(req.Path)
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, fmt.Sprintf("reading %s: %v", req.Path, err))
		return
	}
	if adminContentHash(current) != req.ExpectedHash {
		writeAdminError(w, http.StatusConflict, "file changed on disk; reload and merge")
		return
	}
	if _, err := config.LoadConfigSourcesWithOverlay(h.deps.ConfigPath, h.deps.ConfigDir, req.Path, []byte(req.Content)); err != nil {
		writeAdminError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := writeConfigAtomic(req.Path, current, []byte(req.Content)); err != nil {
		writeAdminError(w, http.StatusInternalServerError, "failed to write config file")
		return
	}
	if h.deps.Reload != nil {
		go h.deps.Reload()
	}
	writeAdminJSON(w, http.StatusOK, map[string]any{
		"hash":      adminContentHash([]byte(req.Content)),
		"reloading": true,
	})
}

// writeConfigAtomic replaces the file behind path with content. The original
// content is first copied to <real path>.bak. Symlinks are resolved so the
// link itself is kept and the target is replaced.
func writeConfigAtomic(path string, original, content []byte) error {
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	info, err := os.Stat(realPath)
	if err != nil {
		return err
	}
	mode := info.Mode().Perm()

	dir := filepath.Dir(realPath)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(realPath)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once renamed

	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return err
	}
	if err := writeFileSynced(realPath+".bak", original, mode); err != nil {
		return err
	}
	return os.Rename(tmpName, realPath)
}

func writeFileSynced(path string, data []byte, mode fs.FileMode) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}
