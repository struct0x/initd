package initd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// Scope provides registration capabilities scoped to a named component.
// It carries the component's context and methods for registering
// cleanup hooks and health probes. All registrations are automatically named
// after the parent [Value] or [Exec] component.
//
// The context returned by [Scope.Context] carries the component name.
// Use [Scope.Logger] to log with an automatic "component" attribution.
type Scope struct {
	Logger *slog.Logger

	app  *App
	ctx  context.Context
	name string
}

// Context returns the context for this component.
// It carries the component name for automatic log attribution
// via [slog.InfoContext].
func (s *Scope) Context() context.Context {
	return s.ctx
}

// OnExit registers a teardown hook for this component.
// It will run in LIFO order:
// - after all Go tasks have drained,
// - and in the case of [Exec] when [Scope.Run] exits.
func (s *Scope) OnExit(fn func(context.Context) error) {
	name := s.name
	s.app.OnExit(name, func(ctx context.Context) error {
		s.Logger.InfoContext(ctx, "teardown")
		return fn(ctx)
	})
}

// Readiness registers a readiness health check for this component.
func (s *Scope) Readiness(fn func(context.Context) error, opts ...ProbeOption) {
	s.app.probes.register(readinessProbe, s.name, fn, opts...)
}

// Liveness registers a liveness health check for this component.
func (s *Scope) Liveness(fn func(context.Context) error, opts ...ProbeOption) {
	s.app.probes.register(livenessProbe, s.name, fn, opts...)
}

// Go spawns a supervised goroutine tied to this component.
// If fn returns a non-context error, the application shuts down.
// The goroutine is tracked by the app's drain group, so shutdown
// waits for it to finish.
// It should respect the context cancellation, and exit gracefully.
// Return an error from fn (other than context.Canceled) will cause the app to shut down
func (s *Scope) Go(name string, fn func(ctx context.Context) error) {
	s.app.wg.Add(1)
	go func() {
		err := s.run(name, fn)

		s.app.wg.Done()

		if err != nil && !errors.Is(err, context.Canceled) {
			s.Logger.ErrorContext(s.ctx, "failed", "error", err, "task", name)
			_ = s.app.lc.Exit(fmt.Errorf("task %q: %s: %w", s.name, name, err))
			return
		}

		s.Logger.InfoContext(s.ctx, "stopped", "task", name)
	}()
}

func (s *Scope) run(name string, fn func(ctx context.Context) error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			s.Logger.ErrorContext(s.ctx, "panic", "error", r, "task", name)
			err = fmt.Errorf("task %q: panic: %v", s.name, r)
		}
	}()

	err = fn(s.ctx)
	return err
}

// Run hands a long-running task to initd and returns immediately.
// The task is expected to block until the context is canceled; if it returns for any reason other than
// context cancellation, initd triggers a graceful shutdown.
// May only be called once per [Scope].
// OnExit hooks must be registered before calling Run.
func (s *Scope) Run(task func(context.Context) error) error {
	s.app.wg.Add(1)
	go func() {
		err := s.run(s.name, task)
		s.app.wg.Done()

		if errors.Is(err, context.Canceled) {
			s.Logger.InfoContext(s.ctx, "stopped")
			return
		}

		if err != nil {
			s.Logger.ErrorContext(s.ctx, "stopped", "error", err)
		} else {
			s.Logger.InfoContext(s.ctx, "stopped")
		}

		if err == nil {
			err = fmt.Errorf("%s: task returned unexpectedly", s.name)
		}

		_ = s.app.lc.Exit(err)
	}()

	return nil
}

func newScope(app *App, name string, ctx context.Context) *Scope {
	return &Scope{
		Logger: app.Logger.With("component", name),
		app:    app,
		ctx:    withComponent(ctx, name),
		name:   name,
	}
}

// Value acquires a resource by running fn immediately with the app's context.
// The [Scope] provides the context, logger, and methods for registering
// cleanup hooks and health probes scoped to this component.
func Value[T any](app *App, name string, fn func(*Scope) (T, error)) (val T, err error) {
	s := newScope(app, name, app.lc.Context())

	select {
	case <-app.lc.Stopping():
		app.Logger.WarnContext(s.ctx, "skipped: app is shutting down")
		var zero T
		return zero, app.lc.Exit(nil)
	default:
	}

	start := time.Now()

	defer func() {
		if r := recover(); r != nil {
			s.Logger.ErrorContext(s.ctx, "panic", "error", r, "duration", time.Since(start))
			err = app.lc.Exit(fmt.Errorf("%s: %w", name, err))
			if app.errorLinger > 0 {
				time.Sleep(app.errorLinger)
			}
		}
	}()

	val, err = fn(s)
	d := time.Since(start)

	if err != nil {
		s.Logger.ErrorContext(s.ctx, "failed", "error", err, "duration", d)
		err = app.lc.Exit(fmt.Errorf("%s: %w", name, err))
		if app.errorLinger > 0 {
			time.Sleep(app.errorLinger)
		}
		return val, err
	}

	s.Logger.InfoContext(s.ctx, "ready", "duration", d)
	return val, nil
}

// Exec runs a void setup step immediately with the app's context.
// Like [Value], but for operations that don't return a value (migrations, cache warming, etc.).
// You can use [Scope.Run] to run a long-running task.
// Example:
//
//	err := initd.Exec(app, "worder", func(s *initd.Scope) error {
//			//....
//			return s.Run(worker.Run)
//	})
func Exec(app *App, name string, fn func(*Scope) error) error {
	_, err := Value(app, name, func(s *Scope) (struct{}, error) {
		return struct{}{}, fn(s)
	})

	return err
}
