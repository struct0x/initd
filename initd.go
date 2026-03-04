/*
Package initd provides an opinionated service bootstrap.
In its basic API provides a way to easily create values, spawn long-lived resources, and track their lifecycle and shutdown procedures.
*/
package initd

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/struct0x/envconfig"
	"github.com/struct0x/exitplan"
)

// App is the central handle for a service bootstrapped with initd.
// Create one with [New], register components with [Value] and [Exec] then call [App.Run] to start.
type App struct {
	Name    string
	Version string
	Logger  *slog.Logger

	lc          *exitplan.Exitplan
	probes      *probeRegistry
	wg          sync.WaitGroup
	boot        *Boot
	errorLinger time.Duration
}

type appConfig struct {
	name          string
	version       string
	globalLogger  bool
	logHandler    slog.Handler
	logLevel      slog.Level
	logMiddleware []func(slog.Handler) slog.Handler
	envLookups    []envconfig.LookupEnv
	skipEnvLoad   bool
	boot          *Boot

	startupTimeout  func() time.Duration
	shutdownTimeout func() time.Duration
	errorLinger     time.Duration
}

// WithBoot passes a [Boot] handle to the [App]. The App takes ownership
// of Boot's context - it will be canceled when the App shuts down.
func WithBoot(b *Boot) Option {
	return func(c *appConfig) { c.boot = b }
}

// Option configures [New].
type Option func(*appConfig)

// WithName sets the service name. Used in logs, and in [initdops] package.
func WithName(name string) Option {
	return func(c *appConfig) { c.name = name }
}

// WithVersion sets the service version.
func WithVersion(version string) Option {
	return func(c *appConfig) { c.version = version }
}

// WithLogHandler overrides the default JSON slog handler.
func WithLogHandler(h slog.Handler) Option {
	return func(c *appConfig) { c.logHandler = h }
}

// WithLogLevel sets the minimum log level. Ignored if WithLogHandler is used.
func WithLogLevel(l slog.Level) Option {
	return func(c *appConfig) { c.logLevel = l }
}

// WithGlobalLogger sets the default logger for the entire application.
func WithGlobalLogger(set bool) Option {
	return func(c *appConfig) { c.globalLogger = set }
}

// WithEnvLookup adds custom environment lookup functions.
// For example, envconfig.EnvFileLookup(".env") for .env file support.
func WithEnvLookup(fn ...envconfig.LookupEnv) Option {
	return func(c *appConfig) { c.envLookups = append(c.envLookups, fn...) }
}

// WithoutEnvLoad skips environment loading. Useful for testing
// when the config struct is populated directly.
func WithoutEnvLoad(skip bool) Option {
	return func(c *appConfig) { c.skipEnvLoad = skip }
}

// WithStartupTimeout sets the maximum duration for the setup phase
// (everything between [New] and [App.Run]). Zero means no timeout.
// Note: it's a closure so it can capture config value after reading env.
// Example usage: WithStartupTimeout(func() time.Duration { return cfg.StartupTimeout })
func WithStartupTimeout(d func() time.Duration) Option {
	return func(c *appConfig) { c.startupTimeout = d }
}

// WithShutdownTimeout sets the maximum duration for the teardown phase.
// Zero means no timeout.
// Note: it's a closure so it can capture config value after reading env.
// Example usage: WithShutdownTimeout(func() time.Duration { return cfg.ShutdownTimeout })
func WithShutdownTimeout(d func() time.Duration) Option {
	return func(c *appConfig) { c.shutdownTimeout = d }
}

// WithErrorLinger keeps the process alive for the given duration after a
// fatal setup error, so that log scrapers have time to collect the error
// before the container restarts. Zero (default) means exit immediately.
func WithErrorLinger(d time.Duration) Option {
	return func(c *appConfig) { c.errorLinger = d }
}

// WithLogMiddleware wraps the slog handler with the given middleware.
// Middleware is applied in order: the first registered middleware is innermost.
// Use this to inject trace IDs, request metadata, or other context-carried
// attributes into log records. See [initdotel.LogHandler] for an example.
func WithLogMiddleware(mw func(slog.Handler) slog.Handler) Option {
	return func(c *appConfig) { c.logMiddleware = append(c.logMiddleware, mw) }
}

// New creates a new [App] and populates cfg from environment variables.
// The config struct is available via the cfg pointer immediately after New returns.
func New[C any](cfg *C, opts ...Option) (*App, error) {
	ac := appConfig{
		startupTimeout: func() time.Duration {
			return 0
		},
		shutdownTimeout: func() time.Duration {
			return 0
		},
	}
	for _, o := range opts {
		o(&ac)
	}

	logger := newLogger(&ac)
	if ac.globalLogger {
		slog.SetDefault(logger)
	}

	if !ac.skipEnvLoad {
		lookups := ac.envLookups
		if len(lookups) == 0 {
			lookups = []envconfig.LookupEnv{os.LookupEnv}
		}
		if err := envconfig.Read(cfg, lookups...); err != nil {
			logger.Error("initd: envconfig.Read failed", "err", err)
			if ac.errorLinger > 0 {
				time.Sleep(ac.errorLinger)
			}
			return nil, fmt.Errorf("initd: %w", err)
		}
	}

	lc := exitplan.New(
		exitplan.WithSignal(syscall.SIGINT, syscall.SIGTERM),
		exitplan.WithStartupTimeout(ac.startupTimeout()),
		exitplan.WithTeardownTimeout(ac.shutdownTimeout()),
		exitplan.WithExitError(func(err error) {
			logger.Error("exit callback error", "err", err)
		}),
	)

	app := &App{
		Logger:      logger,
		lc:          lc,
		Name:        ac.name,
		Version:     ac.version,
		probes:      newProbeRegistry(lc.Context()),
		errorLinger: ac.errorLinger,
		boot:        ac.boot,
	}

	app.probes.register(startupProbe, "initd:startup", func(ctx context.Context) error {
		select {
		case <-lc.Started():
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}, probeOneShot())

	app.probes.register(readinessProbe, "initd:ready", func(ctx context.Context) error {
		select {
		case <-lc.Started():
			return nil
		case <-lc.Stopping():
			return fmt.Errorf("shutdown")
		}
	}, ProbeInterval(math.MaxInt64))

	args := make([]any, 0, 4)
	if ac.name != "" {
		args = append(args, "service", ac.name)
	}
	if ac.version != "" {
		args = append(args, "version", ac.version)
	}

	logger.Info("initd: initialized", args...)

	return app, nil
}

func newLogger(cfg *appConfig) *slog.Logger {
	var handler slog.Handler
	if cfg.logHandler != nil {
		handler = cfg.logHandler
	} else {
		handler = slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
			Level: cfg.logLevel,
		})
	}

	handler = &durationHandler{inner: handler}
	handler = &componentHandler{inner: handler}
	for _, mw := range cfg.logMiddleware {
		handler = mw(handler)
	}

	return slog.New(handler)
}

// Context returns the application context. It is canceled when shutdown begins.
// Available immediately after [New]; use it to plumb into your own components.
func (a *App) Context() context.Context {
	return a.lc.Context()
}

// OnExit registers a teardown hook. Hooks run in LIFO order
// after all [Exec] tasks have drained. If the callback does not
// return before the shutdown context expires, it is abandoned.
func (a *App) OnExit(name string, fn func(context.Context) error) {
	a.lc.OnExitWithContextError(func(ctx context.Context) error {
		done := make(chan error, 1)
		go func() { done <- fn(ctx) }()
		select {
		case err := <-done:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}, exitplan.Name(name))
}

// Shutdown triggers a graceful shutdown. Safe to call from any goroutine.
func (a *App) Shutdown() {
	go func() {
		_ = a.lc.Exit(nil)
	}()
}

// Run blocks until shutdown, drains tasks, and runs [OnExit] hooks in LIFO order.
// Returns the cause of shutdown.
func (a *App) Run() error {
	if a.boot != nil {
		a.boot.close()
	}

	// Drain callback: registered last, runs first in LIFO.
	// Waits for all Go tasks to finish before OnExit hooks close resources.
	a.lc.OnExitWithContextError(func(ctx context.Context) error {
		done := make(chan struct{})
		go func() {
			a.wg.Wait()
			close(done)
		}()
		select {
		case <-done:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}, exitplan.Name("initd:drain"))

	a.Logger.Info("initd: running")

	err := a.lc.Run()

	if err != nil && a.errorLinger > 0 {
		a.Logger.Error("initd: failed", "err", err, "linger", a.errorLinger)
		time.Sleep(a.errorLinger)
	}

	return err
}

// CheckReadiness returns the last-known readiness state.
func (a *App) CheckReadiness() ProbeResult {
	return a.probes.check(readinessProbe)
}

// CheckLiveness returns the last-known liveness state.
func (a *App) CheckLiveness() ProbeResult {
	return a.probes.check(livenessProbe)
}

// CheckStartup returns the last-known startup state.
func (a *App) CheckStartup() ProbeResult {
	return a.probes.check(startupProbe)
}
