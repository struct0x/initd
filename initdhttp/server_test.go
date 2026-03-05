package initdhttp_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/struct0x/initd"
	"github.com/struct0x/initd/initdhttp"
)

// listenAddr starts the server and returns its actual address via WithOnListen
func listenAddr(
	t *testing.T,
	srv *http.Server,
	opts ...initdhttp.ServeOption,
) (app *initd.App, addr string) {
	t.Helper()

	app = newApp(t)
	addrCh := make(chan string, 1)
	opts = append(
		opts,
		initdhttp.WithOnListen(func(a net.Addr) {
			addrCh <- a.String()
		}),
	)
	if err := initd.Exec(app, "http", initdhttp.Serve(srv, opts...)); err != nil {
		t.Fatal(err)
	}
	go func() { _ = app.Run() }()
	return app, <-addrCh
}

func TestServe_servesRequests(t *testing.T) {
	srv := &http.Server{
		Addr: "127.0.0.1:0",
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}),
	}
	_, addr := listenAddr(t, srv)

	resp, err := http.Get("http://" + addr)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}
}

func TestServe_withListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()

	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})}
	_, gotAddr := listenAddr(t, srv, initdhttp.WithListener(ln))

	if gotAddr != addr {
		t.Fatalf("addr = %s, want %s", gotAddr, addr)
	}

	resp, err := http.Get("http://" + addr)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}
}

func TestServe_shutdownOnExit(t *testing.T) {
	srv := &http.Server{
		Addr:    "127.0.0.1:0",
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}),
	}
	app, addr := listenAddr(t, srv)
	app.Shutdown()

	deadline := time.After(2 * time.Second)
	for {
		_, err := http.Get("http://" + addr)
		if err != nil {
			return // server is down
		}
		select {
		case <-deadline:
			t.Fatal("server did not shut down")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestServe_withTLS(t *testing.T) {
	tlsCfg, pool := selfSignedTLS(t)

	srv := &http.Server{
		Addr: "127.0.0.1:0",
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}),
	}
	_, addr := listenAddr(t, srv, initdhttp.WithTLSConfig(tlsCfg))

	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool},
	}}
	resp, err := client.Get("https://" + addr)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}
}

func TestServe_withOnListen_firesBeforeServe(t *testing.T) {
	fired := make(chan net.Addr, 1)
	srv := &http.Server{
		Addr:    "127.0.0.1:0",
		Handler: http.NewServeMux(),
	}
	app := newApp(t)
	if err := initd.Exec(
		app,
		"http",
		initdhttp.Serve(srv,
			initdhttp.WithOnListen(func(a net.Addr) { fired <- a }),
		),
	); err != nil {
		t.Fatal(err)
	}
	go func() { _ = app.Run() }()

	select {
	case addr := <-fired:
		if addr == nil {
			t.Fatal("expected non-nil addr")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WithOnListen callback was not called")
	}
}

func TestServe_readinessProbe(t *testing.T) {
	srv := &http.Server{
		Addr:    "127.0.0.1:0",
		Handler: http.NewServeMux(),
	}
	app, _ := listenAddr(t, srv)

	// After bind: http probe is healthy.
	if check := app.CheckReadiness().Checks["http"]; !check.Healthy {
		t.Fatalf("expected http probe healthy after listen: %s", check.Error)
	}

	app.Shutdown()
	deadline := time.After(2 * time.Second)
	for {
		if !app.CheckReadiness().Checks["http"].Healthy {
			return
		}
		select {
		case <-deadline:
			t.Fatal("http probe did not become unhealthy after shutdown")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// selfSignedTLS generates a self-signed cert/key pair for testing.
func selfSignedTLS(t *testing.T) (*tls.Config, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test"},
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
	}
	return tlsCfg, pool
}
