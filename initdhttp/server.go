package initdhttp

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/struct0x/initd"
)

const (
	defaultReadHeaderTimeout = 5 * time.Second
	defaultIdleTimeout       = 120 * time.Second
	defaultMaxHeaderBytes    = 1 << 20 // 1 MiB
)

// ServeOption configures [Serve].
type ServeOption func(*serveConfig)

type serveConfig struct {
	listener  net.Listener
	tlsConfig *tls.Config
	onListen  func(net.Addr)
}

// WithListener provides a pre-bound listener instead of having [Serve] bind one
// via [net.Listen] on [http.Server.Addr].
//
// Use this when the default TCP bind is not appropriate:
//   - Unix domain sockets (net.Listen("unix", "/run/app.sock"))
//   - systemd socket activation (net.FileListener)
//   - Protocol multiplexing (e.g. cmux sub-listeners)
//   - Any custom [net.Listener] wrapper (rate-limiting, connection tracking)
//
// For random-port allocation in tests, set srv.Addr to "host:0" and use
// [WithOnListen] to capture the assigned address — no pre-bound listener needed.
func WithListener(ln net.Listener) ServeOption {
	return func(c *serveConfig) { c.listener = ln }
}

// WithTLSConfig wraps the listener with TLS before calling srv.Serve.
// Works with both a caller-provided [WithListener] and the default TCP bind.
// The TLS certificate must be configured on the [tls.Config] (e.g. via GetCertificate
// or Certificates); cert/key files are not loaded here.
func WithTLSConfig(cfg *tls.Config) ServeOption {
	return func(c *serveConfig) { c.tlsConfig = cfg }
}

// WithOnListen registers a callback that fires once the server socket is bound,
// before [http.Server.Serve] is called. The callback receives the actual listening
// address.
//
// Common uses:
//   - Discovering the OS-assigned port when srv.Addr is "host:0"
//   - Signaling readiness after bind but before the first request
//
// Example (test helper):
//
//	addrCh := make(chan string, 1)
//	initdhttp.Serve(srv, initdhttp.WithOnListen(func(a net.Addr) {
//	    addrCh <- a.String()
//	}))
func WithOnListen(fn func(net.Addr)) ServeOption {
	return func(c *serveConfig) { c.onListen = fn }
}

// Serve returns an [initd.Exec]-compatible callback that runs srv with
// production-safe defaults and shuts it down gracefully when the app context
// is canceled.
//
// Serve always binds the listener before returning control to the caller's
// goroutine, so by the time [initd.App.Run] returns the server is already
// accepting connections.
//
// If srv.BaseContext is nil, Serve injects the scope's context values
// (component name, etc.) so that [slog.InfoContext](r.Context(), ...) in
// handlers automatically includes structured log attributes. If you set
// srv.BaseContext yourself, Serve do not add its values.
//
// Defaults applied when the corresponding field on srv is zero:
//
//   - ReadHeaderTimeout: 5s   (Slowloris protection)
//
//   - IdleTimeout:       120s (keep-alive connection hygiene)
//
//   - MaxHeaderBytes:    1 MiB (header bomb protection)
//
// err := initd.Exec(app, "http", initdhttp.Serve(srv))
func Serve(srv *http.Server, opts ...ServeOption) func(*initd.Scope) error {
	return func(s *initd.Scope) error {
		cfg := &serveConfig{}
		for _, o := range opts {
			o(cfg)
		}

		if srv.ReadHeaderTimeout == 0 {
			srv.ReadHeaderTimeout = defaultReadHeaderTimeout
		}
		if srv.IdleTimeout == 0 {
			srv.IdleTimeout = defaultIdleTimeout
		}
		if srv.MaxHeaderBytes == 0 {
			srv.MaxHeaderBytes = defaultMaxHeaderBytes
		}

		if srv.BaseContext == nil {
			baseCtx := context.WithoutCancel(s.Context())
			srv.BaseContext = func(net.Listener) context.Context {
				return baseCtx
			}
		}

		ln := cfg.listener
		if ln == nil {
			var err error
			ln, err = net.Listen("tcp", srv.Addr)
			if err != nil {
				return err
			}
		}

		if cfg.tlsConfig != nil {
			ln = tls.NewListener(ln, cfg.tlsConfig)
		}

		var ready atomic.Bool
		s.Readiness(func(_ context.Context) error {
			if !ready.Load() {
				return errors.New("not listening")
			}
			return nil
		})

		ready.Store(true)

		if cfg.onListen != nil {
			cfg.onListen(ln.Addr())
		}

		s.Logger.InfoContext(s.Context(), "listening", "addr", ln.Addr().String())
		s.OnExit(func(ctx context.Context) error {
			ready.Store(false)
			return srv.Shutdown(ctx)
		})

		return s.Run(func(ctx context.Context) error {
			errChan := make(chan error, 1)
			go func() {
				if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
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
