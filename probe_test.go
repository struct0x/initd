package initd_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matryer/is"

	"github.com/struct0x/initd"
)

func TestProbes(t *testing.T) {
	probeSuccessFunc := func(ctx context.Context) error {
		return nil
	}

	probeFailureFunc := func(calls *atomic.Int32) func(ctx context.Context) error {
		return func(ctx context.Context) error {
			c := calls.Add(1)
			if c <= 2 {
				return errors.New("warming up")
			}
			return nil
		}
	}

	expectHealthy := func(r initd.ProbeResult) bool {
		return r.Healthy && r.Checks["probe"].Healthy
	}

	expectUnhealthy := func(r initd.ProbeResult) bool {
		return !r.Healthy && !r.Checks["probe"].Healthy
	}

	totalCalls := &atomic.Int32{}

	tests := []struct {
		name      string
		probeType string
		probeFunc func(ctx context.Context) error
		probeOpts []initd.ProbeOption
		check     func(r initd.ProbeResult) bool
	}{
		{
			name:      "readiness_probe",
			probeType: "readiness",
			probeFunc: probeSuccessFunc,
			check:     expectHealthy,
		},
		{
			name:      "liveness_probe",
			probeType: "liveness",
			probeFunc: probeSuccessFunc,
			check:     expectHealthy,
		},
		{
			name:      "fail_after_stays_healthy",
			probeType: "liveness",
			probeFunc: probeFailureFunc(&atomic.Int32{}),
			probeOpts: []initd.ProbeOption{initd.ProbeFailAfter(3)},
			check:     expectHealthy,
		},
		{
			name:      "probe_failed_after_N_calls",
			probeType: "readiness",
			probeFunc: func(ctx context.Context) error {
				totalCalls.Add(1)
				if totalCalls.Load() == 1 {
					return nil
				}

				return errors.New("probe failed")
			},
			check: func(r initd.ProbeResult) bool {
				return totalCalls.Load() >= 3 && expectUnhealthy(r)
			},
			probeOpts: []initd.ProbeOption{initd.ProbeFailAfter(3)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is := is.New(t)
			_, app := createApp(t, is)

			opts := append([]initd.ProbeOption{initd.ProbeInterval(50 * time.Millisecond)}, tt.probeOpts...)

			var checkFunc func() initd.ProbeResult
			switch tt.probeType {
			case "readiness":
				_ = initd.Exec(app, "probe", func(s *initd.Scope) error {
					s.Readiness(tt.probeFunc, opts...)
					return s.Run(func(ctx context.Context) error {
						<-ctx.Done()
						return ctx.Err()
					})
				})

				checkFunc = app.CheckReadiness
			case "liveness":
				_ = initd.Exec(app, "probe", func(s *initd.Scope) error {
					s.Liveness(tt.probeFunc, opts...)
					return s.Run(func(ctx context.Context) error {
						<-ctx.Done()
						return ctx.Err()
					})
				})
				checkFunc = app.CheckLiveness
			default:
				t.Fatalf("unknown probe type: %s", tt.probeType)
			}

			go func() { _ = app.Run() }()

			eventually(t, 3*time.Second, func() bool {
				r := checkFunc()
				return tt.check(r)
			})
		})
	}

	t.Run("startup_probe", func(t *testing.T) {
		is := is.New(t)
		_, app := createApp(t, is)

		is.True(app.CheckStartup().Healthy == false)
		is.True(app.CheckStartup().Checks["initd:startup"].Healthy == false)

		go func() {
			_ = app.Run()
		}()

		eventually(t, 3*time.Second, func() bool {
			r := app.CheckStartup()
			return r.Healthy && r.Checks["initd:startup"].Healthy
		})
	})

	t.Run("initial_delay_defers_evaluation", func(t *testing.T) {
		is := is.New(t)
		_, app := createApp(t, is)

		var evaluated atomic.Bool
		_ = initd.Exec(app, "delayed", func(s *initd.Scope) error {
			s.Liveness(func(ctx context.Context) error {
				evaluated.Store(true)
				return nil
			}, initd.ProbeInterval(50*time.Millisecond), initd.ProbeInitialDelay(200*time.Millisecond))
			return s.Run(func(ctx context.Context) error {
				<-ctx.Done()
				return ctx.Err()
			})
		})

		time.Sleep(100 * time.Millisecond)
		is.True(!evaluated.Load())

		eventually(t, 2*time.Second, func() bool {
			r := app.CheckLiveness()
			delayed, has := r.Checks["delayed"]
			return has && delayed.Healthy
		})
	})

	t.Run("probe_timeout_marks_unhealthy", func(t *testing.T) {
		is := is.New(t)
		_, app := createApp(t, is)

		_ = initd.Exec(app, "probe", func(s *initd.Scope) error {
			s.Readiness(func(ctx context.Context) error {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(200 * time.Millisecond):
					return nil
				}
			}, initd.ProbeInterval(50*time.Millisecond), initd.ProbeTimeout(50*time.Millisecond))
			return s.Run(func(ctx context.Context) error {
				<-ctx.Done()
				return ctx.Err()
			})
		})

		go func() { _ = app.Run() }()

		eventually(t, 3*time.Second, func() bool {
			check, ok := app.CheckReadiness().Checks["probe"]
			return ok && !check.Healthy && check.Duration < 200*time.Millisecond
		})
	})

	t.Run("readiness_unhealthy_after_shutdown", func(t *testing.T) {
		is := is.New(t)
		_, app := createApp(t, is)

		go func() { _ = app.Run() }()

		eventually(t, 2*time.Second, func() bool {
			return app.CheckReadiness().Healthy && app.CheckReadiness().Checks["initd:ready"].Healthy
		})

		app.Shutdown()

		eventually(t, 2*time.Second, func() bool {
			return !app.CheckReadiness().Healthy && !app.CheckReadiness().Checks["initd:ready"].Healthy
		})
	})
}
