package initdhttp_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/struct0x/initd"
	"github.com/struct0x/initd/initdhttp"
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

// trackingTransport implements http.RoundTripper and tracks CloseIdleConnections calls.
type trackingTransport struct {
	idlesClosed bool
}

func (t *trackingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
}

func (t *trackingTransport) CloseIdleConnections() {
	t.idlesClosed = true
}

func TestClient_defaults(t *testing.T) {
	app := newApp(t)
	client, err := initd.Value(app, "http", initdhttp.Client())
	if err != nil {
		t.Fatal(err)
	}
	if client.Timeout != 30*time.Second {
		t.Fatalf("timeout = %v, want 30s", client.Timeout)
	}
	if client.Transport != http.DefaultTransport {
		t.Fatal("expected http.DefaultTransport when no transport set")
	}
}

func TestClient_withTimeout(t *testing.T) {
	app := newApp(t)
	client, err := initd.Value(app, "http", initdhttp.Client(
		initdhttp.WithTimeout(5*time.Second),
	))
	if err != nil {
		t.Fatal(err)
	}
	if client.Timeout != 5*time.Second {
		t.Fatalf("timeout = %v, want 5s", client.Timeout)
	}
}

func TestClient_withTransport(t *testing.T) {
	app := newApp(t)
	rt := &trackingTransport{}
	client, err := initd.Value(app, "http", initdhttp.Client(
		initdhttp.WithTransport(rt),
	))
	if err != nil {
		t.Fatal(err)
	}
	if client.Transport != rt {
		t.Fatal("expected custom transport to be used as-is")
	}
}

func TestClient_withTransportMiddleware(t *testing.T) {
	app := newApp(t)
	base := &trackingTransport{}
	var mwReceived http.RoundTripper

	client, err := initd.Value(app, "http", initdhttp.Client(
		initdhttp.WithTransport(base),
		initdhttp.WithTransportMiddleware(func(rt http.RoundTripper) http.RoundTripper {
			mwReceived = rt
			return &trackingTransport{}
		}),
	))
	if err != nil {
		t.Fatal(err)
	}
	if mwReceived != base {
		t.Fatal("middleware should receive the base transport")
	}
	if client.Transport == base {
		t.Fatal("client should use the wrapped transport, not the base")
	}
}

func TestClient_exitClosesIdleConnections(t *testing.T) {
	app := newApp(t)
	rt := &trackingTransport{}

	_, err := initd.Value(app, "http", initdhttp.Client(
		initdhttp.WithTransport(rt),
	))
	if err != nil {
		t.Fatal(err)
	}

	go func() { _ = app.Run() }()
	app.Shutdown()

	deadline := time.After(2 * time.Second)
	for {
		if rt.idlesClosed {
			return
		}
		select {
		case <-deadline:
			t.Fatal("CloseIdleConnections was not called after shutdown")
		case <-time.After(10 * time.Millisecond):
		}
	}
}
