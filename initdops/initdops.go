package initdops

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"time"

	"github.com/struct0x/initd"
)

// Option configures [Setup].
type Option func(*opsConfig)

// WithAddr sets the listen address for the ops server.
func WithAddr(addr string) Option {
	return func(c *opsConfig) { c.addr = addr }
}

type opsConfig struct {
	addr string
}

// Setup returns an [initd.Exec]-compatible callback that starts an
// ops HTTP server with health probes, version, and pprof endpoints.
//
// Supported endpoints:
//   - /readyz - Readiness probe endpoint. Returns HTTP 200 if the app is ready to serve traffic,
//     HTTP 503 otherwise. Response body contains JSON with probe results.
//   - /livez - Liveness probe endpoint. Returns HTTP 200 if the app is alive,
//     HTTP 503 otherwise. Response body contains JSON with probe results.
//   - /startupz - Startup probe endpoint. Returns HTTP 200 if the app has completed startup,
//     HTTP 503 otherwise. Response body contains JSON with probe results.
//   - /version - Version information endpoint. Returns JSON with app name and version.
//   - /debug/pprof/ - pprof index page for available profiling data.
//   - /debug/pprof/cmdline - Returns the command line invocation of the current program.
//   - /debug/pprof/profile - CPU profile. Use ?seconds=N query parameter to specify duration (default 30s).
//   - /debug/pprof/symbol - Symbol lookup endpoint for resolving program counters to function names.
//   - /debug/pprof/trace - Execution trace. Use ?seconds=N query parameter to specify duration (default 1s).
func Setup(app *initd.App, opts ...Option) func(*initd.Scope) error {
	return func(s *initd.Scope) error {
		cfg := opsConfig{}
		for _, o := range opts {
			o(&cfg)
		}

		mux := http.NewServeMux()
		mux.Handle("/readyz", probeHandler(app.CheckReadiness))
		mux.Handle("/livez", probeHandler(app.CheckLiveness))
		mux.Handle("/startupz", probeHandler(app.CheckStartup))
		mux.Handle("/version", versionHandler(app.Name, app.Version))

		mux.HandleFunc("/debug/pprof/", pprof.Index)
		mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

		srv := &http.Server{
			Addr:              cfg.addr,
			Handler:           mux,
			ReadHeaderTimeout: 5 * time.Second,
		}
		s.OnExit(srv.Shutdown)

		return s.Run(func(ctx context.Context) error {
			slog.InfoContext(s.Context(), "listening", "addr", cfg.addr)

			errChan := make(chan error, 1)
			go func() {
				if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
					errChan <- err
				}
			}()

			select {
			case err := <-errChan:
				return err
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}
}

func probeHandler(check func() initd.ProbeResult) http.Handler {
	type checkResult struct {
		Healthy  bool          `json:"healthy"`
		Duration time.Duration `json:"duration"`
		Error    string        `json:"error,omitempty"`
	}
	type probeResult struct {
		Healthy bool                   `json:"healthy"`
		Checks  map[string]checkResult `json:"checks,omitempty"`
	}

	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		result := check()

		out := probeResult{
			Healthy: result.Healthy,
			Checks:  make(map[string]checkResult, len(result.Checks)),
		}
		for k, v := range result.Checks {
			out.Checks[k] = checkResult{
				Healthy:  v.Healthy,
				Duration: v.Duration,
				Error:    v.Error,
			}
		}

		w.Header().Set("Content-Type", "application/json")
		if !result.Healthy {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		json.NewEncoder(w).Encode(out)
	})
}

func versionHandler(name, version string) http.Handler {
	type info struct {
		Name    string `json:"name,omitempty"`
		Version string `json:"version,omitempty"`
	}
	body, _ := json.Marshal(info{Name: name, Version: version})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	})
}
