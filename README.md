# initd - func main best friend.

Package initd is an opinionated service bootstrap for Go microservices.

- config loading,
- structured logging,
- health probes,
- graceful shutdown,
- and component lifecycle management.

# Quick Start

```go
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"

	"github.com/struct0x/initd"
	"github.com/struct0x/initd/initddb"
)

func main() {
	if err := run(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "%v", err)
	}
}
func run() error {
	var cfg struct {
		Addr        string `env:"ADDR" envDefault:":8080"`
		DatabaseDSN string `env:"DATABASE_DSN"`
	}

	app, err := initd.New(&cfg,
		initd.WithName("my-service"),
		initd.WithVersion("1.0.0"),
	)
	if err != nil {
		return err
	}

	db, err := initd.Value(app, "postgres", initddb.Open(
		initddb.WithDriver("pgx"),
		initddb.WithDSN(cfg.DatabaseDSN),
	))
	if err != nil {
		return err
	}

	if err := initd.Exec(app, "http", func(s *initd.Scope) error {
		srv := &http.Server{Addr: cfg.Addr, Handler: newRouter(db)}
		s.OnExit(srv.Shutdown)
		return s.Run(func(ctx context.Context) error {
			return srv.ListenAndServe()
		})
	}); err != nil {
		return err
	}

	return app.Run()
}

```

# Lifecycle

A service goes through three phases:

### Setup 
`New` loads config from environment variables and creates the App.
Then `Value` and `Exec` initialize components sequentially.

Each component gets a `Scope` with a logger, context, and methods to register cleanup hooks
and health probes. Setup calls are sequential; if any fail, later calls
are skipped and all registered cleanup hooks run immediately.

### Running
`App.Run` blocks and the service serves traffic. Long-running
tasks handed off via `Scope.Run` and `Scope.Go` run concurrently.
Health probes poll in the background.

### Shutdown
Triggered by SIGINT/SIGTERM or `App.Shutdown`. All managed goroutines
drain first, then `Scope.OnExit` hooks fire in LIFO order. The shutdown
timeout is a total budget for the entire teardown; once exceeded, the
process exits regardless of what's still running.

# Components

- `Value` acquires a named resource (database connection, HTTPClient, etc.)
and returns it. 
- `Exec` is the same but for void operations (migrations, starting a server or workers).

Both accept a function that receives a `Scope` and returns an error.

Inside the callback, use `Scope` to:

- Register cleanup with `Scope.OnExit`
- Register health checks with `Scope.Readiness` and `Scope.Liveness`
- Spawn supervised goroutines with `Scope.Go`
- Hand off a blocking task with `Scope.Run`, valid only in `Exec`

All registrations are named after the component.

# Health Probes

Readiness and liveness probes are registered per-component via `Scope.Readiness`
and `Scope.Liveness`. They poll in the background and expose last-known state via `App.CheckReadiness` and `App.CheckLiveness`.

Use `ProbeInterval`, `ProbeTimeout`, and `ProbeFailAfter` to tune behavior.

A startup probe is built-in: it becomes healthy once `App.Run` is called.
Query it via `App.CheckStartup`.

# Two-Phase Boot

Sometimes you need a live connection just to finish loading config: a secret store, a config server, or a parameter store. 
`Minimal` handles this: it loads a small bootstrap config (just enough to connect), and returns a `Boot` handle with its
own context and logger.

```go
var bootCfg struct {
    SecretStoreAddr string `env:"SECRET_STORE_ADDR"`
}
boot, err := initd.Minimal(&bootCfg)

client := connectSecretStore(boot.Context(), bootCfg.SecretStoreAddr)
boot.OnHandoff(client.Close) // closed when App.Run starts

// resolve env vars through the secret store
secretLookup := func(key string) (string, bool) {
    return client.Get(key)
}

var cfg appCfg
app, err := initd.New(&cfg, initd.WithBoot(boot), initd.WithEnvLookup(secretLookup))
```

The secret store client only lives during setup. `Boot.OnHandoff` closes it once
`App.Run` is called and the full config is loaded. See [example/app.go](example/app.go) for a real-world version.

# Logging

initd sets up structured JSON logging via log/slog. Every component's
Scope carries the component name in context, so logs automatically include
a `component` attribute. 

Durations log as strings (e.g. `1.2s`). 

Use `WithLogHandler` to override the default handler, or
`WithLogMiddleware` to wrap it (e.g. for trace ID injection).

## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

