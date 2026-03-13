// Package initdotel provides OpenTelemetry tracing setup for initd services.
//
// It returns an Exec-compatible callback that configures the global
// TracerProvider and TextMapPropagator, and registers shutdown on exit.
//
//	err := initd.Exec(app, "tracing", initdotel.Setup(
//	    initdotel.WithExporter(exporter),
//	    initdotel.WithServiceName("my-service"),
//	))
package initdotel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/go-logr/logr"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.40.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/struct0x/initd"
)

type config struct {
	exporter       sdktrace.SpanExporter
	sampler        sdktrace.Sampler
	resource       *resource.Resource
	serviceName    string
	serviceVersion string
	propagator     propagation.TextMapPropagator
}

// Option configures [Setup].
type Option func(*config)

// WithExporter sets the span exporter. Required.
func WithExporter(e sdktrace.SpanExporter) Option {
	return func(c *config) { c.exporter = e }
}

// WithSampler overrides the default sampler (ParentBased(AlwaysSample)).
func WithSampler(s sdktrace.Sampler) Option {
	return func(c *config) { c.sampler = s }
}

// WithResource overrides the auto-built resource entirely.
func WithResource(r *resource.Resource) Option {
	return func(c *config) { c.resource = r }
}

// WithServiceName sets the service.name resource attribute.
func WithServiceName(name string) Option {
	return func(c *config) { c.serviceName = name }
}

// WithServiceVersion sets the service.version resource attribute.
func WithServiceVersion(version string) Option {
	return func(c *config) { c.serviceVersion = version }
}

// WithPropagator overrides the default text map propagator
// (W3C TraceContext + Baggage).
func WithPropagator(p propagation.TextMapPropagator) Option {
	return func(c *config) { c.propagator = p }
}

// Setup returns an [initd.Exec]-compatible callback that initializes
// OpenTelemetry tracing. It sets the global TracerProvider and
// TextMapPropagator, and registers tp.Shutdown via [initd.Scope.OnExit].
func Setup(opts ...Option) func(*initd.Scope) error {
	return func(s *initd.Scope) error {
		var cfg config
		for _, o := range opts {
			o(&cfg)
		}

		if cfg.exporter == nil {
			return errors.New("initdotel: exporter is required")
		}

		res, err := buildResource(&cfg)
		if err != nil {
			return fmt.Errorf("initdotel: resource: %w", err)
		}

		tpOpts := []sdktrace.TracerProviderOption{
			sdktrace.WithBatcher(cfg.exporter),
			sdktrace.WithResource(res),
		}
		if cfg.sampler != nil {
			tpOpts = append(tpOpts, sdktrace.WithSampler(cfg.sampler))
		}

		tp := sdktrace.NewTracerProvider(tpOpts...)
		otel.SetTracerProvider(tp)
		setOtelLogger(s)

		prop := cfg.propagator
		if prop == nil {
			prop = propagation.NewCompositeTextMapPropagator(
				propagation.TraceContext{},
				propagation.Baggage{},
			)
		}
		otel.SetTextMapPropagator(prop)

		s.OnExit(func(ctx context.Context) error {
			return tp.Shutdown(ctx)
		})

		return nil
	}
}

// LogHandler returns a [slog.Handler] middleware that adds traceID and
// spanID attributes to every log record when a valid span is present
// in the context. Pass to [initd.WithLogMiddleware].
//
//	app, _ := initd.New(&cfg,
//	    initd.WithLogMiddleware(initdotel.LogHandler),
//	)
func LogHandler(inner slog.Handler) slog.Handler {
	return &traceLogHandler{inner: inner}
}

type traceLogHandler struct {
	inner slog.Handler
}

func (h *traceLogHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *traceLogHandler) Handle(ctx context.Context, r slog.Record) error {
	sc := trace.SpanFromContext(ctx).SpanContext()
	if sc.IsValid() {
		r.AddAttrs(
			slog.String("traceID", sc.TraceID().String()),
			slog.String("spanID", sc.SpanID().String()),
		)
	}
	return h.inner.Handle(ctx, r)
}

func (h *traceLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &traceLogHandler{inner: h.inner.WithAttrs(attrs)}
}

func (h *traceLogHandler) WithGroup(name string) slog.Handler {
	return &traceLogHandler{inner: h.inner.WithGroup(name)}
}

var otelLoggerOnce sync.Once

// setOtelLogger routes OTel SDK internal logs to the scope's logger.
func setOtelLogger(s *initd.Scope) {
	otelLoggerOnce.Do(func() {
		otel.SetLogger(logr.FromSlogHandler(s.Logger.Handler()))
	})
}

func buildResource(cfg *config) (*resource.Resource, error) {
	if cfg.resource != nil {
		return cfg.resource, nil
	}

	var attrs []attribute.KeyValue
	if cfg.serviceName != "" {
		attrs = append(attrs, semconv.ServiceName(cfg.serviceName))
	}
	if cfg.serviceVersion != "" {
		attrs = append(attrs, semconv.ServiceVersion(cfg.serviceVersion))
	}

	return resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(semconv.SchemaURL, attrs...),
	)
}
