package initdhttp

import (
	"context"
	"net/http"
	"time"

	"github.com/struct0x/initd"
)

const defaultTimeout = 30 * time.Second

type config struct {
	timeout     time.Duration
	transport   http.RoundTripper
	transportMW func(http.RoundTripper) http.RoundTripper
}

// Option configures [Client].
type Option func(*config)

// WithTimeout sets the request timeout. Default is 30s.
func WithTimeout(d time.Duration) Option {
	return func(c *config) { c.timeout = d }
}

// WithTransport sets the base transport before any middleware is applied.
func WithTransport(rt http.RoundTripper) Option {
	return func(c *config) { c.transport = rt }
}

// WithTransportMiddleware wraps the transport with the given middleware.
// Use this to plug in tracing instrumentation from any provider:
//
//	initdhttp.WithTransportMiddleware(otelhttp.NewTransport)
func WithTransportMiddleware(mw func(http.RoundTripper) http.RoundTripper) Option {
	return func(c *config) { c.transportMW = mw }
}

// Client returns a [initd.Value]-compatible callback that builds an
// [http.Client] with sensible defaults. If [WithTransportMiddleware]
// is provided, the transport is wrapped for tracing.
func Client(opts ...Option) func(*initd.Scope) (*http.Client, error) {
	return func(s *initd.Scope) (*http.Client, error) {
		cfg := config{timeout: defaultTimeout}
		for _, o := range opts {
			o(&cfg)
		}

		transport := cfg.transport
		if transport == nil {
			transport = http.DefaultTransport
		}

		if cfg.transportMW != nil {
			transport = cfg.transportMW(transport)
		}

		client := &http.Client{
			Timeout:   cfg.timeout,
			Transport: transport,
		}

		s.OnExit(func(_ context.Context) error {
			client.CloseIdleConnections()
			return nil
		})

		s.Logger.InfoContext(s.Context(), "configured", "timeout", cfg.timeout)
		return client, nil
	}
}
