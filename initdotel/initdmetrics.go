package initdotel

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"

	"github.com/struct0x/initd"
)

// SetupMetrics returns an [initd.Exec]-compatible callback that initializes
// OpenTelemetry metrics. It sets the global MeterProvider and registers
// mp.Shutdown via [initd.Scope.OnExit].
//
// The reader determines the export strategy:
//   - [sdkmetric.NewPeriodicReader] for push-based exporters (OTLP)
//   - Prometheus reader (go.opentelemetry.io/otel/exporters/prometheus) for pull-based scraping
//
// [WithServiceName], [WithServiceVersion], and [WithResource] are honoured.
// [WithExporter], [WithSampler], and [WithPropagator] are ignored.
//
//	err := initd.Exec(app, "metrics", initdotel.SetupMetrics(
//	    sdkmetric.NewPeriodicReader(otlpExporter),
//	    initdotel.WithServiceName("my-service"),
//	))
func SetupMetrics(reader sdkmetric.Reader, opts ...Option) func(*initd.Scope) error {
	return func(s *initd.Scope) error {
		var cfg config
		for _, o := range opts {
			o(&cfg)
		}

		res, err := buildResource(&cfg)
		if err != nil {
			return fmt.Errorf("initdotel: resource: %w", err)
		}

		mp := sdkmetric.NewMeterProvider(
			sdkmetric.WithReader(reader),
			sdkmetric.WithResource(res),
		)
		otel.SetMeterProvider(mp)
		setOtelLogger(s)

		s.OnExit(func(ctx context.Context) error {
			return mp.Shutdown(ctx)
		})

		return nil
	}
}
