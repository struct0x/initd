package initddb_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"
	"time"

	"github.com/struct0x/initd"
	"github.com/struct0x/initd/initddb"
)

// fakeDriver is a minimal database/sql driver for testing.
// DSN "fail" makes Ping return an error; any other DSN succeeds.
func init() {
	sql.Register("fakedb", fakeDriver{})
}

type fakeDriver struct{}

func (fakeDriver) Open(dsn string) (driver.Conn, error) {
	return &fakeConn{fail: dsn == "fail"}, nil
}

type fakeConn struct{ fail bool }

func (c *fakeConn) Ping(_ context.Context) error {
	if c.fail {
		return errors.New("connection refused")
	}
	return nil
}
func (c *fakeConn) Prepare(string) (driver.Stmt, error) { return fakeStmt{}, nil }
func (c *fakeConn) Close() error                        { return nil }
func (c *fakeConn) Begin() (driver.Tx, error)           { return nil, errors.New("not supported") }

type fakeStmt struct{}

func (fakeStmt) Close() error                               { return nil }
func (fakeStmt) NumInput() int                              { return -1 }
func (fakeStmt) Exec([]driver.Value) (driver.Result, error) { return nil, nil }
func (fakeStmt) Query([]driver.Value) (driver.Rows, error)  { return nil, nil }

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

func TestOpen_success(t *testing.T) {
	app := newApp(t)
	db, err := initd.Value(app, "db", initddb.Open(
		initddb.WithDriver("fakedb"),
		initddb.WithDSN("ok"),
	))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if db == nil {
		t.Fatal("expected non-nil db")
	}
}

func TestOpen_pingFailure(t *testing.T) {
	app := newApp(t)
	_, err := initd.Value(app, "db", initddb.Open(
		initddb.WithDriver("fakedb"),
		initddb.WithDSN("fail"),
	))
	if err == nil {
		t.Fatal("expected error on ping failure")
	}
}

func TestOpen_openFuncError(t *testing.T) {
	app := newApp(t)
	wantErr := errors.New("dial: connection refused")
	_, err := initd.Value(app, "db", initddb.Open(
		initddb.WithOpenFunc(func(_, _ string) (*sql.DB, error) {
			return nil, wantErr
		}),
	))
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected %v, got %v", wantErr, err)
	}
}

func TestOpen_poolSettings(t *testing.T) {
	app := newApp(t)
	db, err := initd.Value(app, "db", initddb.Open(
		initddb.WithDriver("fakedb"),
		initddb.WithDSN("ok"),
		initddb.WithMaxOpenConns(7),
		initddb.WithMaxIdleConns(3),
		initddb.WithConnMaxLifetime(30*time.Minute),
		initddb.WithConnMaxIdleTime(5*time.Minute),
	))
	if err != nil {
		t.Fatal(err)
	}
	// MaxOpenConnections is the only pool setting exposed by db.Stats().
	// The others (MaxIdleConns, ConnMaxLifetime, ConnMaxIdleTime) have no
	// getter on *sql.DB — we verify they're accepted without error above.
	if got := db.Stats().MaxOpenConnections; got != 7 {
		t.Fatalf("MaxOpenConnections = %d, want 7", got)
	}
}

func TestOpen_exitClosesDB(t *testing.T) {
	app := newApp(t)
	db, err := initd.Value(app, "db", initddb.Open(
		initddb.WithDriver("fakedb"),
		initddb.WithDSN("ok"),
	))
	if err != nil {
		t.Fatal(err)
	}

	go func() { _ = app.Run() }()
	app.Shutdown()

	deadline := time.After(2 * time.Second)
	for {
		if db.PingContext(context.Background()) != nil {
			return
		}
		select {
		case <-deadline:
			t.Fatal("db was not closed after shutdown")
		case <-time.After(10 * time.Millisecond):
		}
	}
}
