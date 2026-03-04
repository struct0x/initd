package initdotel_test

import (
	"go.opentelemetry.io/otel"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/struct0x/initd"
	"github.com/struct0x/initd/initdotel"
)

// ExampleSetup shows the minimal wiring to enable distributed tracing.
// In production replace tracetest.NewInMemoryExporter with an OTLP exporter.
func ExampleSetup() {
	var cfg struct{}
	app, _ := initd.New(&cfg, initd.WithoutEnvLoad(true))
	defer app.Shutdown()
	defer otel.SetTracerProvider(tracenoop.NewTracerProvider())

	_ = initd.Exec(app, "tracing", initdotel.Setup(
		initdotel.WithExporter(tracetest.NewInMemoryExporter()),
		initdotel.WithServiceName("my-service"),
		initdotel.WithServiceVersion("1.0.0"),
	))
	// otel.Tracer("my-pkg").Start(ctx, "operation") is now ready globally.
}

// ExampleSetup_customPropagator shows how to override the default W3C propagator.
func ExampleSetup_customPropagator() {
	var cfg struct{}
	app, _ := initd.New(&cfg, initd.WithoutEnvLoad(true))
	defer app.Shutdown()
	defer otel.SetTracerProvider(tracenoop.NewTracerProvider())
	defer otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator())

	_ = initd.Exec(app, "tracing", initdotel.Setup(
		initdotel.WithExporter(tracetest.NewInMemoryExporter()),
		initdotel.WithPropagator(propagation.TraceContext{}), // W3C only, no Baggage
	))
}

// ExampleSetupMetrics shows wiring push-based metrics via a periodic reader.
// Swap sdkmetric.NewManualReader for sdkmetric.NewPeriodicReader(otlpExporter)
// in production, or use the Prometheus reader for pull-based scraping.
func ExampleSetupMetrics() {
	var cfg struct{}
	app, _ := initd.New(&cfg, initd.WithoutEnvLoad(true))
	defer app.Shutdown()
	defer otel.SetMeterProvider(metricnoop.NewMeterProvider())

	_ = initd.Exec(app, "metrics", initdotel.SetupMetrics(
		sdkmetric.NewManualReader(),
		initdotel.WithServiceName("my-service"),
		initdotel.WithServiceVersion("1.0.0"),
	))
	// otel.Meter("my-pkg").Int64Counter("requests_total") is now ready globally.
}

// ExampleSetupMetrics_prometheus shows how to wire a Prometheus pull exporter.
// Requires go.opentelemetry.io/otel/exporters/prometheus in your go.mod.
//
//	import otelprom "go.opentelemetry.io/otel/exporters/prometheus"
//
//	promExporter, _ := otelprom.New()
//	_ = initd.Exec(app, "metrics", initdotel.SetupMetrics(promExporter))
//
// Then mount promhttp.Handler() on /metrics in your HTTP server.
func ExampleSetupMetrics_prometheus() {}

// ExampleLogHandler shows how to inject traceID/spanID into structured logs.
func ExampleLogHandler() {
	var cfg struct{}
	app, _ := initd.New(&cfg,
		initd.WithoutEnvLoad(true),
		initd.WithLogMiddleware(initdotel.LogHandler),
	)
	defer app.Shutdown()
	_ = app
	// All loggers derived from app.Logger (including per-scope loggers) will
	// now emit "traceID" and "spanID" attributes when a span is active in ctx.
}
