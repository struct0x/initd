package initdotel_test

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/struct0x/initd"
	"github.com/struct0x/initd/initdotel"
)

func resetMetricGlobals(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		otel.SetMeterProvider(noop.NewMeterProvider())
	})
}

func TestSetupMetrics_setsGlobalMeterProvider(t *testing.T) {
	resetMetricGlobals(t)
	app := newApp(t)

	if err := initd.Exec(app, "metrics", initdotel.SetupMetrics(
		sdkmetric.NewManualReader(),
		initdotel.WithServiceName("test-svc"),
	)); err != nil {
		t.Fatal(err)
	}

	if _, ok := otel.GetMeterProvider().(*sdkmetric.MeterProvider); !ok {
		t.Fatal("expected global MeterProvider to be *sdkmetric.MeterProvider")
	}
}

func TestSetupMetrics_onExitShutsDownProvider(t *testing.T) {
	resetMetricGlobals(t)
	app := newApp(t)

	spy := &spyMetricExporter{}
	reader := sdkmetric.NewPeriodicReader(spy, sdkmetric.WithInterval(time.Hour))
	if err := initd.Exec(app, "metrics", initdotel.SetupMetrics(reader)); err != nil {
		t.Fatal(err)
	}

	go func() { _ = app.Run() }()
	app.Shutdown()

	deadline := time.After(2 * time.Second)
	for {
		if spy.stopped {
			return
		}
		select {
		case <-deadline:
			t.Fatal("MeterProvider was not shut down after app shutdown")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

type spyMetricExporter struct {
	stopped bool
}

func (e *spyMetricExporter) Temporality(sdkmetric.InstrumentKind) metricdata.Temporality {
	return metricdata.CumulativeTemporality
}

func (e *spyMetricExporter) Aggregation(k sdkmetric.InstrumentKind) sdkmetric.Aggregation {
	return sdkmetric.DefaultAggregationSelector(k)
}
func (e *spyMetricExporter) Export(context.Context, *metricdata.ResourceMetrics) error { return nil }
func (e *spyMetricExporter) ForceFlush(context.Context) error                          { return nil }
func (e *spyMetricExporter) Shutdown(context.Context) error {
	e.stopped = true
	return nil
}
