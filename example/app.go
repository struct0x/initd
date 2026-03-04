package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/struct0x/initd"
	"github.com/struct0x/initd/initdhttp"
)

type Config struct {
	Addr            string        `env:"ADDR" envDefault:":8080"`
	MaxStartupTime  time.Duration `env:"MAX_STARTUP_TIME" envDefault:"30s"`
	ShutdownTimeout time.Duration `env:"SHUTDOWN_TIMEOUT" envDefault:"10s"`
}

func main() {
	if err := run(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	// Phase 1: load a minimal bootstrap config to connect to the secret store.
	var bootCfg struct {
		SecretStoreAddr string `env:"SECRET_STORE_ADDR"`
	}
	boot, err := initd.Minimal(&bootCfg)
	if err != nil {
		return err
	}

	// Connect to the secret store using the boot context, then use it to
	// resolve env vars for the full config.
	// client := secrets.Connect(boot.Context(), bootCfg.SecretStoreAddr)
	// boot.OnHandoff(client.Close)
	secretLookup := func(key string) (string, bool) {
		// return client.Get(key)
		return os.LookupEnv(key)
	}

	// Phase 2: load full config, create app.
	var cfg Config
	app, err := initd.New(&cfg,
		initd.WithBoot(boot),
		initd.WithName("example"),
		initd.WithVersion("0.1.0"),
		initd.WithEnvLookup(secretLookup),
		initd.WithStartupTimeout(func() time.Duration { return cfg.MaxStartupTime }),
		initd.WithShutdownTimeout(func() time.Duration { return cfg.ShutdownTimeout }),
	)
	if err != nil {
		return err
	}

	// Shared HTTP client with tracing middleware.
	client, err := initd.Value(app, "http-client", initdhttp.Client(
		initdhttp.WithTimeout(10*time.Second),
		// initdhttp.WithTransportMiddleware(otelhttp.NewTransport),
	))
	if err != nil {
		return err
	}
	_ = client

	// HTTP server with graceful shutdown and readiness probe.
	if err := initd.Exec(app, "http", func(s *initd.Scope) error {
		srv := &http.Server{
			Addr:    cfg.Addr,
			Handler: newRouter(),
		}
		s.Readiness(func(ctx context.Context) error {
			return nil // replace with a real dependency check
		})
		s.OnExit(srv.Shutdown)
		return s.Run(func(ctx context.Context) error {
			s.Logger.Info("listening", "addr", cfg.Addr)
			if err := srv.ListenAndServe(); err != http.ErrServerClosed {
				return err
			}
			return nil
		})
	}); err != nil {
		return err
	}

	return app.Run()
}

func newRouter() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return mux
}
