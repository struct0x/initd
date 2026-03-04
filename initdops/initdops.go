package initdops

import (
	"context"
	"encoding/json"
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

		// pprof — always on; ops server is already opt-in and internal-only.
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
			return srv.ListenAndServe()
		})
	}
}

func probeHandler(check func() initd.ProbeResult) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		result := check()

		w.Header().Set("Content-Type", "application/json")
		if !result.Healthy {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		json.NewEncoder(w).Encode(result)
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
