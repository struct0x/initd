package initd_test

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/matryer/is"

	"github.com/struct0x/initd"
)

func TestInitdNew(t *testing.T) {
	t.Run("basic_setup", func(t *testing.T) {
		t.Setenv("ENVIRONMENT", "test")

		is := is.New(t)

		var cfg struct {
			Environment string `env:"ENVIRONMENT"`
		}

		logs := newCaptureHandler()
		app, err := initd.New(
			&cfg,
			initd.WithName("test-app"),
			initd.WithVersion("1.0.0"),
			initd.WithLogHandler(logs),
		)
		is.NoErr(err)
		is.True(app != nil)
		is.Equal(cfg.Environment, "test")

		logs.AssertLines(t,
			L(slog.LevelInfo, "initd: initialized").
				With("version", "1.0.0").
				With("service", "test-app"),
		)
	})

	t.Run("env_load_error", func(t *testing.T) {
		is := is.New(t)

		var cfg struct {
			Environment string `env:"ENVIRONMENT" envRequired:"true"`
		}

		logs := newCaptureHandler()
		app, err := initd.New(
			&cfg,
			initd.WithName("test-app"),
			initd.WithVersion("1.0.0"),
			initd.WithLogHandler(logs),
		)
		is.Equal(err.Error(), "initd: envconfig: required field \"ENVIRONMENT\" is empty")
		is.True(app == nil)

		logs.AssertLines(t,
			L(slog.LevelError, "initd: envconfig.Read failed").
				With("err", "envconfig: required field \"ENVIRONMENT\" is empty"),
		)
	})

	t.Run("env_load_skip_load", func(t *testing.T) {
		t.Setenv("ENVIRONMENT", "NOT_EMPTY")
		is := is.New(t)

		var cfg struct {
			Environment string `env:"ENVIRONMENT" envRequired:"true"`
		}

		logs := newCaptureHandler()
		app, err := initd.New(
			&cfg,
			initd.WithName("test-app"),
			initd.WithVersion("1.0.0"),
			initd.WithoutEnvLoad(true),
			initd.WithLogHandler(logs),
		)
		is.NoErr(err)
		is.True(app != nil)
		is.Equal(cfg.Environment, "")

		logs.AssertLines(t,
			L(slog.LevelInfo, "initd: initialized").
				With("service", "test-app").
				With("version", "1.0.0"),
		)
	})
}

func TestInitdBoot(t *testing.T) {
	is := is.New(t)
	logs := newCaptureHandler()

	t.Setenv("ENVIRONMENT", "test")

	var cfg struct {
		Environment string `env:"ENVIRONMENT"`
	}

	boot, err := initd.Minimal(&cfg)
	is.NoErr(err)
	is.True(boot != nil)

	var onHandoffCalled bool
	boot.OnHandoff(func() {
		onHandoffCalled = true
	})

	app, err := initd.New(
		&cfg,
		initd.WithBoot(boot),
		initd.WithLogHandler(logs),
	)
	is.NoErr(err)
	is.True(app != nil)

	go func() {
		_ = app.Run()
	}()

	eventually(t, 2*time.Second, func() bool {
		return app.CheckReadiness().Healthy
	})

	is.True(onHandoffCalled)

	logs.AssertLines(t,
		L(slog.LevelInfo, "initd: initialized"),
		L(slog.LevelInfo, "initd: running"),
	)
}

func TestValue(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		is := is.New(t)

		logs, app := createApp(t, is)
		value, err := initd.Value(app, "test-value", func(scope *initd.Scope) (string, error) {
			is.True(scope != nil)
			return "__SCOPE_CREATED__", nil
		})

		is.NoErr(err)
		is.Equal(value, "__SCOPE_CREATED__")

		logs.AssertLines(t,
			L(slog.LevelInfo, "initd: initialized").
				With("service", "test-app").
				With("version", "1.0.0"),
			L(slog.LevelInfo, "starting").
				With("component", "test-value"),
			L(slog.LevelInfo, "ready").
				With("component", "test-value"),
		)
	})

	t.Run("error", func(t *testing.T) {
		is := is.New(t)
		logs, app := createApp(t, is)

		var onExitCalled bool

		_, err := initd.Value(app, "test-value", func(scope *initd.Scope) (string, error) {
			is.True(scope != nil)
			scope.OnExit(func(ctx context.Context) error {
				onExitCalled = true
				return nil
			})

			return "", errors.New("test error")
		})
		is.Equal(err.Error(), "test-value: test error")
		is.True(onExitCalled)

		logs.AssertLines(t,
			L(slog.LevelInfo, "initd: initialized").
				With("service", "test-app").
				With("version", "1.0.0"),
			L(slog.LevelInfo, "starting").
				With("component", "test-value"),
			L(slog.LevelError, "failed").
				With("component", "test-value").
				With("error", "test error"),
			L(slog.LevelInfo, "teardown").
				With("component", "test-value"),
		)
	})

	t.Run("panic_recovery", func(t *testing.T) {
		is := is.New(t)
		logs, app := createApp(t, is)

		_, err := initd.Value(app, "panicking", func(s *initd.Scope) (string, error) {
			panic("boom")
		})
		is.True(err != nil)
		is.True(strings.Contains(err.Error(), "panic"))

		logs.AssertLines(t,
			L(slog.LevelInfo, "initd: initialized").
				With("service", "test-app").
				With("version", "1.0.0"),
			L(slog.LevelInfo, "starting").
				With("component", "panicking"),
			L(slog.LevelError, "panic").
				With("component", "panicking").
				With("error", "boom"),
		)
	})

	t.Run("skips_after_failure", func(t *testing.T) {
		is := is.New(t)
		_, app := createApp(t, is)

		_, _ = initd.Value(app, "first", func(s *initd.Scope) (string, error) {
			return "", errors.New("fail")
		})

		called := false
		_, err := initd.Value(app, "second", func(s *initd.Scope) (string, error) {
			called = true
			return "nope", nil
		})
		is.True(err != nil)
		is.True(!called)
	})

	t.Run("cleanup_on_subsequent_failure", func(t *testing.T) {
		is := is.New(t)
		_, app := createApp(t, is)

		var cleaned atomic.Bool

		_, err := initd.Value(app, "db", func(s *initd.Scope) (string, error) {
			s.OnExit(func(ctx context.Context) error {
				cleaned.Store(true)
				return nil
			})
			return "conn", nil
		})
		is.NoErr(err)

		_, _ = initd.Value(app, "cache", func(s *initd.Scope) (string, error) {
			return "", errors.New("redis down")
		})

		is.True(cleaned.Load())
	})

	t.Run("readiness_probe_via_scope", func(t *testing.T) {
		is := is.New(t)
		_, app := createApp(t, is)

		_, err := initd.Value(app, "db", func(s *initd.Scope) (string, error) {
			s.Readiness(func(ctx context.Context) error {
				return nil
			}, initd.ProbeInterval(50*time.Millisecond))
			return "conn", nil
		})
		is.NoErr(err)

		go func() { _ = app.Run() }()

		eventually(t, 2*time.Second, func() bool {
			r := app.CheckReadiness()
			_, hasDB := r.Checks["db"]
			return r.Healthy && hasDB
		})

		app.Shutdown()
	})

	t.Run("lifo_teardown_order", func(t *testing.T) {
		is := is.New(t)
		_, app := createApp(t, is)

		var order []string
		var mu sync.Mutex

		for _, name := range []string{"first", "second", "third"} {
			name := name
			_, err := initd.Value(app, name, func(s *initd.Scope) (string, error) {
				s.OnExit(func(ctx context.Context) error {
					mu.Lock()
					order = append(order, name)
					mu.Unlock()
					return nil
				})
				return name, nil
			})
			is.NoErr(err)
		}

		_, _ = initd.Value(app, "fail", func(s *initd.Scope) (string, error) {
			return "", errors.New("fail")
		})

		mu.Lock()
		is.Equal(order, []string{"third", "second", "first"})
		mu.Unlock()
	})

	t.Run("scope_go_error_triggers_shutdown", func(t *testing.T) {
		is := is.New(t)
		_, app := createApp(t, is)

		err := initd.Exec(app, "parent", func(s *initd.Scope) error {
			s.Go("worker", func(ctx context.Context) error {
				return errors.New("worker crashed")
			})
			return s.Run(func(ctx context.Context) error {
				<-ctx.Done()
				return ctx.Err()
			})
		})
		is.NoErr(err)

		runErr := app.Run()
		is.True(runErr != nil)
		is.True(strings.Contains(runErr.Error(), "worker crashed"))
	})

	t.Run("scope_go_panic_triggers_shutdown", func(t *testing.T) {
		is := is.New(t)
		_, app := createApp(t, is)

		err := initd.Exec(app, "parent", func(s *initd.Scope) error {
			s.Go("panicking-worker", func(ctx context.Context) error {
				panic("worker panic")
			})
			return s.Run(func(ctx context.Context) error {
				<-ctx.Done()
				return ctx.Err()
			})
		})
		is.NoErr(err)

		runErr := app.Run()
		is.True(runErr != nil)
		is.True(strings.Contains(runErr.Error(), "panic"))
	})

	t.Run("scope_go_context_canceled_is_clean", func(t *testing.T) {
		is := is.New(t)
		_, app := createApp(t, is)

		var workerStopped atomic.Bool

		err := initd.Exec(app, "parent", func(s *initd.Scope) error {
			s.Go("graceful-worker", func(ctx context.Context) error {
				<-ctx.Done()
				workerStopped.Store(true)
				return ctx.Err()
			})
			return s.Run(func(ctx context.Context) error {
				<-ctx.Done()
				return ctx.Err()
			})
		})
		is.NoErr(err)

		go func() { _ = app.Run() }()

		eventually(t, 2*time.Second, func() bool {
			return app.CheckReadiness().Healthy
		})

		app.Shutdown()

		eventually(t, 2*time.Second, func() bool {
			return workerStopped.Load()
		})
	})

	t.Run("scope_run_nil_return_triggers_shutdown", func(t *testing.T) {
		is := is.New(t)
		_, app := createApp(t, is)

		err := initd.Exec(app, "finite", func(s *initd.Scope) error {
			return s.Run(func(ctx context.Context) error {
				return nil // unexpected return
			})
		})
		is.NoErr(err)

		runErr := app.Run()
		is.True(runErr != nil)
		is.True(strings.Contains(runErr.Error(), "task returned unexpectedly"))
	})

	t.Run("scope_run_error_triggers_shutdown", func(t *testing.T) {
		is := is.New(t)
		_, app := createApp(t, is)

		err := initd.Exec(app, "crashing", func(s *initd.Scope) error {
			s.OnExit(func(ctx context.Context) error {
				return nil
			})
			return s.Run(func(ctx context.Context) error {
				return errors.New("listen: address in use")
			})
		})
		is.NoErr(err)

		runErr := app.Run()
		is.True(runErr != nil)
		is.True(strings.Contains(runErr.Error(), "address in use"))
	})

	t.Run("scope_go_drain_before_on_exit", func(t *testing.T) {
		is := is.New(t)
		_, app := createApp(t, is)

		var sequence []string
		var mu sync.Mutex
		append := func(s string) {
			mu.Lock()
			sequence = append(sequence, s)
			mu.Unlock()
		}

		err := initd.Exec(app, "server", func(s *initd.Scope) error {
			s.OnExit(func(ctx context.Context) error {
				append("on_exit")
				return nil
			})
			s.Go("background", func(ctx context.Context) error {
				<-ctx.Done()
				time.Sleep(50 * time.Millisecond) // simulate drain work
				append("go_drained")
				return ctx.Err()
			})
			return s.Run(func(ctx context.Context) error {
				<-ctx.Done()
				return ctx.Err()
			})
		})
		is.NoErr(err)

		go func() { _ = app.Run() }()

		eventually(t, 2*time.Second, func() bool {
			return app.CheckReadiness().Healthy
		})

		app.Shutdown()

		eventually(t, 2*time.Second, func() bool {
			mu.Lock()
			defer mu.Unlock()
			return len(sequence) >= 2
		})

		mu.Lock()
		is.Equal(sequence, []string{"go_drained", "on_exit"})
		mu.Unlock()
	})

	t.Run("error_linger_delays_on_failure", func(t *testing.T) {
		is := is.New(t)
		t.Setenv("ENVIRONMENT", "test")

		var cfg struct {
			Environment string `env:"ENVIRONMENT"`
		}

		linger := 100 * time.Millisecond
		app, err := initd.New(&cfg,
			initd.WithLogHandler(newCaptureHandler()),
			initd.WithErrorLinger(linger),
			initd.WithoutEnvLoad(true),
		)
		is.NoErr(err)

		start := time.Now()
		_, err = initd.Value(app, "slow-fail", func(s *initd.Scope) (string, error) {
			return "", errors.New("boom")
		})
		elapsed := time.Since(start)

		is.True(err != nil)
		is.True(elapsed >= linger)
	})
}

func createApp(t *testing.T, is *is.I) (*captureHandler, *initd.App) {
	t.Setenv("ENVIRONMENT", "test")

	var cfg struct {
		Environment string `env:"ENVIRONMENT"`
	}

	logs := newCaptureHandler()
	app, err := initd.New(
		&cfg,
		initd.WithName("test-app"),
		initd.WithVersion("1.0.0"),
		initd.WithLogHandler(logs),
	)
	is.NoErr(err)
	is.True(app != nil)
	is.Equal(cfg.Environment, "test")
	t.Cleanup(app.Shutdown)

	return logs, app
}

type logLine struct {
	Level   slog.Level
	Message string
	Attrs   map[string]string // flattened key→value
}

func L(level slog.Level, message string) logLine {
	return logLine{Level: level, Message: message, Attrs: make(map[string]string)}
}

type captureHandler struct {
	mu      *sync.Mutex
	records *[]logLine
	prefix  map[string]string
}

func (h *captureHandler) WithGroup(name string) slog.Handler {
	return h
}

func newCaptureHandler() *captureHandler {
	return &captureHandler{
		mu:      &sync.Mutex{},
		records: &[]logLine{},
		prefix:  make(map[string]string),
	}
}

func (h *captureHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	lr := logLine{
		Level:   r.Level,
		Message: r.Message,
		Attrs:   make(map[string]string),
	}

	for k, v := range h.prefix {
		lr.Attrs[k] = v
	}

	r.Attrs(func(a slog.Attr) bool {
		lr.Attrs[a.Key] = a.Value.String()
		return true
	})
	h.mu.Lock()
	*h.records = append(*h.records, lr)
	h.mu.Unlock()
	return nil
}

func (h *captureHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &captureHandler{
		records: h.records,
		mu:      h.mu,
		prefix:  appendAttrs(h.prefix, attrs),
	}
}

func (h *captureHandler) AssertLines(t *testing.T, expected ...logLine) {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()

	diff := cmp.Diff(expected, *h.records,
		cmpopts.IgnoreMapEntries(func(k string, _ string) bool {
			return k == "duration"
		}),
	)
	if diff != "" {
		t.Fatalf("log mismatch (-expected +got):\n%s", diff)
	}
}

func (l logLine) With(key, value string) logLine {
	l.Attrs[key] = value
	return l
}

func appendAttrs(base map[string]string, attrs []slog.Attr) map[string]string {
	merged := make(map[string]string, len(base)+len(attrs))
	for k, v := range base {
		merged[k] = v
	}
	for _, a := range attrs {
		merged[a.Key] = a.Value.String()
	}
	return merged
}

func eventually(t *testing.T, timeout time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.After(timeout)
	interval := 10 * time.Millisecond
	for {
		if fn() {
			return
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for condition")
		case <-time.After(interval):
			interval = min(interval*2, 500*time.Millisecond)
		}
	}
}
