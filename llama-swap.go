package main

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/mostlygeek/llama-swap/docs"
	"github.com/mostlygeek/llama-swap/internal/auth"
	"github.com/mostlygeek/llama-swap/internal/config"
	"github.com/mostlygeek/llama-swap/internal/docagent"
	"github.com/mostlygeek/llama-swap/internal/event"
	"github.com/mostlygeek/llama-swap/internal/hw"
	"github.com/mostlygeek/llama-swap/internal/logmon"
	"github.com/mostlygeek/llama-swap/internal/mcptools/sourcecode"
	"github.com/mostlygeek/llama-swap/internal/perf"
	"github.com/mostlygeek/llama-swap/internal/process"
	"github.com/mostlygeek/llama-swap/internal/server"
	"github.com/mostlygeek/llama-swap/internal/store/sqlite"
	"github.com/mostlygeek/llama-swap/internal/swaputil"
	"github.com/mostlygeek/llama-swap/internal/tailcat"
	configwatcher "github.com/mostlygeek/llama-swap/internal/watcher"
)

var (
	version = "0"
	commit  = "abcd1234"
	date    = "unknown"
)

const shutdownTimeout = 30 * time.Second

// docsConfigSchema stays at the repository root because it is the public
// schema URL and is also used by deployment tooling.
//
//go:embed config-schema.json
var docsConfigSchema []byte

// logTimeFormats maps the cfg.LogTimeFormat value to a Go time layout. An
// unset or unrecognised value yields "" — no timestamp prefix.
var logTimeFormats = map[string]string{
	"ansic":       time.ANSIC,
	"unixdate":    time.UnixDate,
	"rubydate":    time.RubyDate,
	"rfc822":      time.RFC822,
	"rfc822z":     time.RFC822Z,
	"rfc850":      time.RFC850,
	"rfc1123":     time.RFC1123,
	"rfc1123z":    time.RFC1123Z,
	"rfc3339":     time.RFC3339,
	"rfc3339nano": time.RFC3339Nano,
	"kitchen":     time.Kitchen,
	"stamp":       time.Stamp,
	"stampmilli":  time.StampMilli,
	"stampmicro":  time.StampMicro,
	"stampnano":   time.StampNano,
}

func configStorePath(cfg config.Config) string {
	if cfg.Store == nil {
		return ""
	}
	return strings.TrimSpace(cfg.Store.Path)
}

// authStorePath returns the auth database path (contract section 0): store.path
// when configured, otherwise llama-swap-auth.db next to the config file, or in
// the config directory when only -config-dir is given. It returns "" when no
// location can be derived, which selects the in-memory store.
func authStorePath(cfg config.Config, configPath, configDir string) string {
	if p := configStorePath(cfg); p != "" {
		return p
	}
	switch {
	case configPath != "":
		return filepath.Join(filepath.Dir(configPath), "llama-swap-auth.db")
	case configDir != "":
		return filepath.Join(configDir, "llama-swap-auth.db")
	}
	return ""
}

// openAuthStore opens the auth store, falling back to an in-memory store (with a
// warning) when the file cannot be opened.
// adminSetupTokenPath is a root-only file next to the auth database. The
// setup token is never written to the log stream: the UI serves /logs from
// the same process and the service manager captures stdout.
func adminSetupTokenPath(cfg config.Config, configPath, configDir string) string {
	if p := authStorePath(cfg, configPath, configDir); p != "" {
		return filepath.Join(filepath.Dir(p), "llama-swap-admin-setup-token")
	}
	return filepath.Join(os.TempDir(), "llama-swap-admin-setup-token")
}

func openAuthStore(cfg config.Config, configPath, configDir string) (*sqlite.Store, error) {
	path := authStorePath(cfg, configPath, configDir)
	st, err := sqlite.New(path)
	if err != nil && path != "" {
		slog.Warn("failed to open auth database, falling back to in-memory auth store", "path", path, "error", err)
		return sqlite.New("")
	}
	return st, err
}

// runResetAdmin implements -reset-admin: it deletes the admin account, recovery
// codes and sessions (API keys are kept) and returns the process exit code.
func runResetAdmin(configPath, configDir string, out io.Writer) int {
	cfg, err := config.LoadConfigSources(configPath, configDir)
	if err != nil {
		fmt.Fprintf(out, "failed to load config: %v\n", err)
		return 1
	}
	st, err := openAuthStore(cfg, configPath, configDir)
	if err != nil {
		fmt.Fprintf(out, "failed to open auth store: %v\n", err)
		return 1
	}
	defer st.Close()
	svc, err := auth.New(context.Background(), st.Auth(), auth.Options{})
	if err != nil {
		fmt.Fprintf(out, "failed to initialise auth: %v\n", err)
		return 1
	}
	if err := svc.ResetAdmin(context.Background()); err != nil {
		fmt.Fprintf(out, "failed to reset admin: %v\n", err)
		return 1
	}
	fmt.Fprintln(out, "admin account reset: run the setup flow again to create a new admin")
	return 0
}

func configureTailcatListener(cfg *config.Config, keyPath string) error {
	enabled := keyPath != ""
	cfg.SetTailcatEnabled(enabled)
	if !enabled {
		return nil
	}
	if cfg.Tailcat == nil || len(cfg.Tailcat.Models) == 0 {
		return fmt.Errorf("-listen-tailcat requires tailcat.models to define at least one exposed model")
	}
	if len(cfg.Tailcat.Allow) == 0 {
		slog.Warn(`tailcat.allow is empty: every Tailcat client will get 403 Forbidden; add client node keys, or "*" to allow any client`)
	}
	return nil
}

func loadTailcatListenerKey(keyPath string, enabled bool) (*tailcat.PrivateKey, error) {
	if !enabled {
		return nil, nil
	}
	privateKey, err := tailcat.LoadPrivateKey(keyPath, true)
	if err != nil {
		return nil, fmt.Errorf("load -listen-tailcat key: %w", err)
	}
	return privateKey, nil
}

// runValidate loads the configuration from the given sources and prints a
// short human-readable result to out. It returns 0 when the config loads
// without error and 1 otherwise. It does not start the server, detect
// hardware, or open a listener.
func runValidate(configPath, configDir string, out io.Writer) int {
	cfg, err := config.LoadConfigSources(configPath, configDir)
	if err != nil {
		fmt.Fprintf(out, "config validation failed: %v\n", err)
		return 1
	}
	fmt.Fprintf(out, "config is valid: %d model(s), %d peer(s)\n", len(cfg.Models), len(cfg.Peers))
	return 0
}

func main() {
	flagConfig := flag.String("config", "", "path to config file")
	flagConfigDir := flag.String("config-dir", "", "directory of *.yml/*.yaml config files (additive to -config)")
	flagListen := flag.String("listen", "", "listen address (default :8080 or :8443 for TLS)")
	flagCertFile := flag.String("tls-cert-file", "", "TLS certificate file")
	flagKeyFile := flag.String("tls-key-file", "", "TLS key file")
	flagListenTailcat := flag.String("listen-tailcat", "", "path to Tailcat server PrivateKey JSON file")
	flagVersion := flag.Bool("version", false, "show version and exit")
	flagWatchConfig := flag.Bool("watch-config", false, "reload config on file change")
	flagValidate := flag.Bool("validate", false, "validate the config file and exit (without starting the server)")
	flagResetAdmin := flag.Bool("reset-admin", false, "delete the admin account, recovery codes, and sessions, then exit (API keys are kept)")
	flag.Parse()

	if *flagVersion {
		fmt.Printf("version: %s (%s), built at %s\n", version, commit, date)
		os.Exit(0)
	}

	if *flagConfig == "" && *flagConfigDir == "" {
		slog.Error("at least one of -config or -config-dir must be provided")
		os.Exit(1)
	}

	if *flagValidate {
		code := runValidate(*flagConfig, *flagConfigDir, os.Stdout)
		os.Exit(code)
	}

	if *flagResetAdmin {
		os.Exit(runResetAdmin(*flagConfig, *flagConfigDir, os.Stdout))
	}

	useTLS := *flagCertFile != "" || *flagKeyFile != ""
	if (*flagCertFile != "" && *flagKeyFile == "") || (*flagCertFile == "" && *flagKeyFile != "") {
		slog.Error("both -tls-cert-file and -tls-key-file must be provided for TLS")
		os.Exit(1)
	}

	listenAddr := *flagListen
	if listenAddr == "" {
		if useTLS {
			listenAddr = ":8443"
		} else {
			listenAddr = ":8080"
		}
	}

	cfg, loadedFingerprint, err := loadSources(*flagConfig, *flagConfigDir)
	if err != nil {
		slog.Error("failed to load config", "config", *flagConfig, "config-dir", *flagConfigDir, "error", err)
		os.Exit(1)
	}
	if err := configureTailcatListener(&cfg, *flagListenTailcat); err != nil {
		slog.Error("invalid Tailcat listener configuration", "error", err)
		os.Exit(1)
	}
	tailcatPrivateKey, err := loadTailcatListenerKey(*flagListenTailcat, cfg.TailcatEnabled())
	if err != nil {
		slog.Error("failed to load -listen-tailcat key", "error", err)
		os.Exit(1)
	}

	// Loggers are wired per cfg.LogToStdout: the selected streams feed
	// logs.MuxLogs, which owns the combined history served by /logs. They
	// outlive config reloads, so a LogToStdout change requires a restart to
	// take effect.
	logs, err := server.NewLoggers(cfg.LogToStdout)
	if err != nil {
		slog.Error("failed to create loggers", "error", err)
		os.Exit(1)
	}
	proxyLog := logs.ProxyLogs

	applyLogSettings := func(cfg config.Config) {
		level := logmon.LevelInfo
		switch strings.ToLower(strings.TrimSpace(cfg.LogLevel)) {
		case "debug":
			level = logmon.LevelDebug
		case "warn":
			level = logmon.LevelWarn
		case "error":
			level = logmon.LevelError
		}
		timeFormat := logTimeFormats[strings.ToLower(strings.TrimSpace(cfg.LogTimeFormat))]
		logs.SetLogLevel(level)
		logs.SetLogTimeFormat(timeFormat)
	}

	applyLogSettings(cfg)
	proxyLog.Debugf("PID: %d", os.Getpid())

	// Hardware describes the inference host and remains stable for the life of
	// this process, including config reloads. Detection is best effort so an
	// unavailable platform probe never prevents llama-swap from starting.
	var hardwareSnapshot *hw.HardwareSnapshot
	detectCtx, detectCancel := context.WithTimeout(context.Background(), 10*time.Second)
	detectedHardware, detectErr := hw.Detect(detectCtx, version)
	detectCancel()
	if detectErr != nil {
		proxyLog.Warnf("hardware detection unavailable: %v", detectErr)
	} else {
		hardwareSnapshot = &detectedHardware
	}

	// On Windows, bind the process tree to a Job Object so every upstream
	// process is reaped when llama-swap exits — even on a forced kill. No-op
	// elsewhere. Non-fatal: a failure just falls back to per-process teardown.
	if err := process.SetupTreeCleanup(); err != nil {
		proxyLog.Warnf("failed to set up process tree cleanup: %v", err)
	}

	// perfMon outlives config reloads; its config is updated in place.
	var perfMon *perf.Monitor
	if !cfg.Performance.Disabled {
		perfMon, err = perf.New(cfg.Performance, proxyLog)
		if err != nil {
			slog.Error("failed to create performance monitor", "error", err)
			os.Exit(1)
		}
		perfMon.Start()
	} else {
		proxyLog.Info("performance monitoring is disabled")
	}

	buildInfo := server.BuildInfo{Version: version, Commit: commit, Date: date}

	// Indexed once and shared by every Server instance, including the ones a
	// hot config reload creates: the documentation is immutable and does not
	// depend on cfg.
	referenceDocs := docagent.NewWithSchema(docs.Files, docsConfigSchema)

	// Shared the same way, so the source checkout behind the code__* MCP
	// tools is fetched at most once per process, on first use.
	sourceCode := sourcecode.NewProvider(sourcecode.Options{
		Ref:     sourcecode.RefForVersion(version),
		Version: version,
		Commit:  commit,
		Logf:    proxyLog.Infof,
	})

	initialStorePath := configStorePath(cfg)
	initialStore, err := sqlite.New(initialStorePath)
	if err != nil {
		slog.Error("failed to create store", "error", err)
		os.Exit(1)
	}

	authStore, err := openAuthStore(cfg, *flagConfig, *flagConfigDir)
	if err != nil {
		slog.Error("failed to open auth store", "error", err)
		os.Exit(1)
	}
	authSvc, err := auth.New(context.Background(), authStore.Auth(), auth.Options{})
	if err != nil {
		slog.Error("failed to initialise auth", "error", err)
		os.Exit(1)
	}
	setupTokenPath := adminSetupTokenPath(cfg, *flagConfig, *flagConfigDir)
	if token, ok := authSvc.SetupToken(); ok {
		// The token must not enter the log stream: /logs is served by the same
		// process and the dinit unit writes stdout to a file. Put it in a
		// root-only file and log only the path.
		if err := os.WriteFile(setupTokenPath, []byte(token+"\n"), 0o600); err != nil {
			slog.Warn("failed to write admin setup token", "path", setupTokenPath, "error", err)
		} else {
			proxyLog.Infof("admin setup token written to %s (mode 0600); open /ui/ to create the admin account", setupTokenPath)
		}
	} else {
		_ = os.Remove(setupTokenPath)
	}
	var reload, reloadIfChanged func()
	adminDeps := server.AdminDeps{
		Auth:       authSvc,
		ConfigPath: *flagConfig,
		ConfigDir:  *flagConfigDir,
		Reload:     func() { go reloadIfChanged() },
	}
	initialSrv, err := server.New(cfg, logs, perfMon, initialStore, buildInfo, hardwareSnapshot, referenceDocs, sourceCode, adminDeps)
	if err != nil {
		slog.Error("failed to create server", "error", err)
		initialStore.Close()
		os.Exit(1)
	}

	// activeSrv is swapped atomically during hot reload.
	var activeMu sync.RWMutex
	activeSrv := initialSrv
	activeStore := initialStore
	activeStorePath := initialStorePath

	tailcatHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		activeMu.RLock()
		srv := activeSrv
		activeMu.RUnlock()
		srv.ServeTailcatHTTP(w, r)
	})
	tailcatLogger := func(cfg config.Config) tailcat.Logger {
		if cfg.Tailcat != nil && cfg.Tailcat.Debug {
			return proxyLog
		}
		return nil
	}
	startTailcat := func(cfg config.Config) (*tailcat.Server, error) {
		ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		return tailcat.Start(ctx, tailcat.ServerOptions{
			PrivateKey: tailcatPrivateKey,
			Handler:    tailcatHandler,
			Logger:     tailcatLogger(cfg),
		})
	}

	var activeTailcat *tailcat.Server
	if cfg.TailcatEnabled() {
		activeTailcat, err = startTailcat(cfg)
		if err != nil {
			slog.Error("failed to start Tailcat server", "error", err)
			initialSrv.Shutdown(shutdownTimeout)
			initialStore.Close()
			os.Exit(1)
		}
	}
	if activeTailcat != nil {
		initialSrv.SetTailcatAddress(activeTailcat.Address())
		proxyLog.Infof("Tailcat listening on virtual TCP port 80: %s", activeTailcat.Address())
	}

	httpServer := &http.Server{
		Addr: listenAddr,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			activeMu.RLock()
			srv := activeSrv
			activeMu.RUnlock()
			srv.ServeHTTP(w, r)
		}),
	}

	// reloadMu guards reloading and reloadPending. A call that arrives while a
	// reload runs sets reloadPending instead of being dropped, so the running
	// loop checks the sources again once it finishes. loadedFingerprint is only
	// accessed from the reload loop, which runs one reload at a time.
	var reloading, reloadPending, reloadPendingForce bool
	var reloadMu sync.Mutex

	// reloadOnce applies the current config sources. Unless force is set, it
	// skips the reload when the sources match what is already live.
	reloadOnce := func(force bool) {
		fingerprint, fpErr := sourcesFingerprint(*flagConfig, *flagConfigDir)
		if !force && fpErr == nil && fingerprint == loadedFingerprint {
			proxyLog.Info("config sources unchanged since last reload, skipping")
			return
		}

		proxyLog.Info("reloading configuration")

		newCfg, loaded, err := loadSources(*flagConfig, *flagConfigDir)
		if err != nil {
			proxyLog.Warnf("failed to reload config: %v", err)
			return
		}
		if err := configureTailcatListener(&newCfg, *flagListenTailcat); err != nil {
			proxyLog.Warnf("failed to reload config: %v", err)
			return
		}

		newStorePath := configStorePath(newCfg)
		activeMu.RLock()
		currentStore := activeStore
		currentStorePath := activeStorePath
		currentTailcat := activeTailcat
		activeMu.RUnlock()

		newStore := currentStore
		storeChanged := newStorePath != currentStorePath
		if storeChanged {
			newStore, err = sqlite.New(newStorePath)
			if err != nil {
				proxyLog.Warnf("failed to create new store during reload: %v", err)
				return
			}
		}

		newSrv, err := server.New(newCfg, logs, perfMon, newStore, buildInfo, hardwareSnapshot, referenceDocs, sourceCode, adminDeps)
		if err != nil {
			proxyLog.Warnf("failed to build new server during reload: %v", err)
			if storeChanged {
				newStore.Close()
			}
			return
		}

		if currentTailcat != nil {
			newSrv.SetTailcatAddress(currentTailcat.Address())
			// The listener is never restarted: connected Tailcat clients do not
			// reconnect to a restarted server. tailcat.allow is enforced by
			// newSrv per request; only the debug logger needs updating here.
			currentTailcat.SetLogger(tailcatLogger(newCfg))
		}

		activeMu.Lock()
		old := activeSrv
		oldStore := activeStore
		activeSrv = newSrv
		activeStore = newStore
		activeStorePath = newStorePath
		activeMu.Unlock()
		loadedFingerprint = loaded

		applyLogSettings(newCfg)
		if perfMon != nil {
			perfMon.UpdateConfig(newCfg.Performance)
		}

		if err := old.Shutdown(shutdownTimeout); err != nil {
			proxyLog.Warnf("error shutting down old server during reload: %v", err)
		}
		if storeChanged {
			if err := oldStore.Close(); err != nil {
				proxyLog.Warnf("error closing old store during reload: %v", err)
			}
		}

		// Notify UI after a short delay so it can refresh model state.
		time.AfterFunc(3*time.Second, func() {
			event.Emit(swaputil.ConfigFileChangedEvent{State: swaputil.ReloadingStateEnd})
		})

		proxyLog.Info("configuration reloaded")
	}

	// reloadConfig runs reloadOnce, then any reload requested while it ran.
	reloadConfig := func(force bool) {
		reloadMu.Lock()
		if reloading {
			reloadPending = true
			reloadPendingForce = reloadPendingForce || force
			reloadMu.Unlock()
			return
		}
		reloading = true
		reloadMu.Unlock()
		for {
			reloadOnce(force)
			reloadMu.Lock()
			if !reloadPending {
				reloading = false
				reloadMu.Unlock()
				return
			}
			force = reloadPendingForce
			reloadPending, reloadPendingForce = false, false
			reloadMu.Unlock()
		}
	}

	reload = func() { reloadConfig(true) }
	reloadIfChanged = func() { reloadConfig(false) }

	watcherCtx, watcherCancel := context.WithCancel(context.Background())
	defer watcherCancel()

	if *flagWatchConfig {
		proxyLog.Info("watching configuration for changes (poll-based, 2s interval)")

		if *flagConfig != "" {
			absConfigPath, err := filepath.Abs(*flagConfig)
			if err != nil {
				slog.Error("watch-config: failed to resolve config path", "error", err)
				os.Exit(1)
			}
			go func() {
				(&configwatcher.Watcher{
					Path:     absConfigPath,
					Interval: configwatcher.DefaultInterval,
					OnChange: reloadIfChanged,
				}).Run(watcherCtx)
			}()
		}

		if *flagConfigDir != "" {
			absConfigDir, err := filepath.Abs(*flagConfigDir)
			if err != nil {
				slog.Error("watch-config: failed to resolve config-dir path", "error", err)
				os.Exit(1)
			}
			go func() {
				(&configwatcher.DirWatcher{
					Path:     absConfigDir,
					Interval: configwatcher.DefaultInterval,
					OnChange: reloadIfChanged,
				}).Run(watcherCtx)
			}()
		}
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	go func() {
		var startErr error
		if useTLS {
			proxyLog.Infof("llama-swap listening with TLS on https://%s", listenAddr)
			startErr = httpServer.ListenAndServeTLS(*flagCertFile, *flagKeyFile)
		} else {
			proxyLog.Infof("llama-swap listening on http://%s", listenAddr)
			startErr = httpServer.ListenAndServe()
		}
		if startErr != nil && !errors.Is(startErr, http.ErrServerClosed) {
			slog.Error("http server error", "error", startErr)
			os.Exit(1)
		}
	}()

	if !swaputil.IsLoopbackAddr(listenAddr) {
		_, port, _ := net.SplitHostPort(listenAddr)
		proxyLog.Infof("llama-swap is reachable by all hosts on the network, use -listen localhost:%s to restrict to loopback only", port)
	}

	exitChan := make(chan struct{})

	go func() {
		for {
			sig := <-sigChan
			switch sig {
			case syscall.SIGHUP:
				proxyLog.Info("received SIGHUP, reloading config")
				go reload()
			case syscall.SIGINT, syscall.SIGTERM:
				proxyLog.Infof("received signal %v, shutting down", sig)
				watcherCancel()

				// Backstop against a stalled shutdown: force the process to
				// exit once the whole graceful sequence has had its full budget.
				// On Windows the Job Object reaps upstream processes on exit, so
				// a forced exit still cleans up rather than orphaning children.
				go func() {
					time.Sleep(shutdownTimeout + 5*time.Second)
					proxyLog.Warnf("graceful shutdown exceeded %v, forcing exit", shutdownTimeout)
					os.Exit(1)
				}()

				activeMu.RLock()
				srv := activeSrv
				st := activeStore
				tailcatRuntime := activeTailcat
				activeMu.RUnlock()

				// Close long-lived SSE streams first so httpServer.Shutdown can
				// drain without blocking on them for the full timeout.
				srv.CloseStreams()

				// Both phases share a single deadline so total shutdown is
				// bounded by shutdownTimeout rather than 2x it.
				deadline := time.Now().Add(shutdownTimeout)
				shutdownCtx, cancel := context.WithDeadline(context.Background(), deadline)
				defer cancel()
				if err := httpServer.Shutdown(shutdownCtx); err != nil {
					proxyLog.Warnf("http server shutdown error: %v", err)
				}
				if tailcatRuntime != nil {
					if err := tailcatRuntime.Close(shutdownCtx); err != nil {
						proxyLog.Warnf("Tailcat server shutdown error: %v", err)
					}
				}

				// Clamp the remaining budget to a small positive value: a
				// non-positive timeout makes the router fall back to its own
				// healthCheckTimeout, which would defeat the shared deadline.
				remaining := time.Until(deadline)
				if remaining <= 0 {
					remaining = time.Millisecond
				}
				if err := srv.Shutdown(remaining); err != nil {
					proxyLog.Warnf("router shutdown error: %v", err)
				}

				if perfMon != nil {
					perfMon.Stop()
				}
				if err := st.Close(); err != nil {
					proxyLog.Warnf("store shutdown error: %v", err)
				}

				if err := authStore.Close(); err != nil {
					proxyLog.Warnf("auth store shutdown error: %v", err)
				}

				close(exitChan)
				return
			}
		}
	}()

	<-exitChan
	proxyLog.Info("shutdown complete")
}

// sourcesFingerprint hashes the path and bytes of every config source file in
// merge order. Equal fingerprints mean LoadConfigSources yields the same
// config, whatever the file mtimes are.
func sourcesFingerprint(configPath, configDir string) (string, error) {
	files, err := config.ConfigSourceFiles(configPath, configDir)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%s\x00%d\x00", f, len(data))
		h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// loadSources loads the config sources and returns the fingerprint of the
// bytes they hold. The fingerprint is "" if the sources changed during the
// load or could not be read, so the next check treats them as unloaded.
func loadSources(configPath, configDir string) (config.Config, string, error) {
	before, beforeErr := sourcesFingerprint(configPath, configDir)
	cfg, err := config.LoadConfigSources(configPath, configDir)
	if err != nil {
		return cfg, "", err
	}
	after, afterErr := sourcesFingerprint(configPath, configDir)
	if beforeErr != nil || afterErr != nil || before != after {
		return cfg, "", nil
	}
	return cfg, before, nil
}
