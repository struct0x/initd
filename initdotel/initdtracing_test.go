package initdotel_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/struct0x/initd"
	"github.com/struct0x/initd/initdotel"
)

func newApp(t *testing.T) *initd.App {
	t.Helper()
	var cfg struct{}
	app, err := initd.New(&cfg, initd.WithoutEnvLoad(true))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Shutdown)
	return app
}

func resetGlobals(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		otel.SetTracerProvider(noop.NewTracerProvider())
		otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator())
	})
}

func TestSetup_noExporter(t *testing.T) {
	app := newApp(t)
	err := initd.Exec(app, "tracing", initdotel.Setup())
	if err == nil {
		t.Fatal("expected error when exporter is not set")
	}
}

func TestSetup_setsGlobalTracerProvider(t *testing.T) {
	resetGlobals(t)
	app := newApp(t)

	exporter := tracetest.NewInMemoryExporter()
	if err := initd.Exec(app, "tracing", initdotel.Setup(
		initdotel.WithExporter(exporter),
		initdotel.WithServiceName("test-svc"),
	)); err != nil {
		t.Fatal(err)
	}

	_, span := otel.Tracer("test").Start(context.Background(), "op")
	span.End()

	// Note: forcing sync since Batcher is async.
	tp := otel.GetTracerProvider().(*sdktrace.TracerProvider)
	_ = tp.ForceFlush(context.Background())

	if len(exporter.GetSpans()) == 0 {
		t.Fatal("expected span to be recorded by global TracerProvider")
	}
}

func TestSetup_customPropagator(t *testing.T) {
	resetGlobals(t)
	app := newApp(t)

	exporter := tracetest.NewInMemoryExporter()
	prop := propagation.TraceContext{}
	if err := initd.Exec(app, "tracing", initdotel.Setup(
		initdotel.WithExporter(exporter),
		initdotel.WithPropagator(prop),
	)); err != nil {
		t.Fatal(err)
	}

	if otel.GetTextMapPropagator() != prop {
		t.Fatal("expected custom propagator to be set globally")
	}
}

type spyExporter struct {
	stopped bool
}

func (e *spyExporter) ExportSpans(context.Context, []sdktrace.ReadOnlySpan) error { return nil }
func (e *spyExporter) Shutdown(context.Context) error {
	e.stopped = true
	return nil
}

func TestSetup_onExitShutsDownProvider(t *testing.T) {
	resetGlobals(t)
	app := newApp(t)

	exporter := &spyExporter{}
	if err := initd.Exec(app, "tracing", initdotel.Setup(
		initdotel.WithExporter(exporter),
	)); err != nil {
		t.Fatal(err)
	}

	go func() { _ = app.Run() }()
	app.Shutdown()

	deadline := time.After(2 * time.Second)
	for {
		if exporter.stopped {
			return
		}
		select {
		case <-deadline:
			t.Fatal("exporter was not stopped after shutdown")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// --- LogHandler tests ---

type captureHandler struct {
	attrs []slog.Attr
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	r.Attrs(func(a slog.Attr) bool {
		h.attrs = append(h.attrs, a)
		return true
	})
	return nil
}
func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(string) slog.Handler      { return h }

func TestLogHandler_withSpan(t *testing.T) {
	inner := &captureHandler{}
	logger := slog.New(initdotel.LogHandler(inner))

	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{1},
		SpanID:     trace.SpanID{1},
		TraceFlags: trace.FlagsSampled,
		Remote:     true,
	})
	ctx := trace.ContextWithSpanContext(context.Background(), sc)

	logger.InfoContext(ctx, "hello")

	attrs := make(map[string]string)
	for _, a := range inner.attrs {
		attrs[a.Key] = a.Value.String()
	}

	if _, ok := attrs["traceID"]; !ok {
		t.Fatal("expected traceID attribute")
	}
	if _, ok := attrs["spanID"]; !ok {
		t.Fatal("expected spanID attribute")
	}
}

func TestLogHandler_withoutSpan(t *testing.T) {
	inner := &captureHandler{}
	logger := slog.New(initdotel.LogHandler(inner))

	logger.InfoContext(context.Background(), "hello")

	for _, a := range inner.attrs {
		if a.Key == "traceID" || a.Key == "spanID" {
			t.Fatalf("unexpected attribute %q added without an active span", a.Key)
		}
	}
}
