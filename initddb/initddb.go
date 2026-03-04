// Package initddb provides an opinionated database connection for initd services.
//
// It opens a [sql.DB], configures the connection pool, registers a readiness
// probe (ping), and closes the connection on exit. Tracing is pluggable
// via [WithOpenFunc].
//
//	db, err := initd.Value(app, "postgres", initddb.Open(
//	    initddb.WithDriver("pgx"),
//	    initddb.WithDSN(cfg.DatabaseURL),
//	    initddb.WithMaxOpenConns(25),
//	))
package initddb

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/struct0x/initd"
)

type config struct {
	driver          string
	dsn             string
	openFunc        func(string, string) (*sql.DB, error)
	maxOpenConns    int
	maxIdleConns    int
	connMaxLifetime time.Duration
	connMaxIdleTime time.Duration
}

// Option configures [Open].
type Option func(*config)

// WithDriver sets the database driver name (e.g. "pgx", "mysql").
func WithDriver(name string) Option {
	return func(c *config) { c.driver = name }
}

// WithDSN sets the data source name / connection string.
func WithDSN(dsn string) Option {
	return func(c *config) { c.dsn = dsn }
}

// WithOpenFunc overrides [sql.Open] with an instrumented opener.
// Use this to plug in tracing from any provider:
//
//	initddb.WithOpenFunc(otelsql.Open)
//	initddb.WithOpenFunc(sqltrace.Open)
func WithOpenFunc(fn func(string, string) (*sql.DB, error)) Option {
	return func(c *config) { c.openFunc = fn }
}

// WithMaxOpenConns sets the maximum number of open connections.
func WithMaxOpenConns(n int) Option {
	return func(c *config) { c.maxOpenConns = n }
}

// WithMaxIdleConns sets the maximum number of idle connections.
func WithMaxIdleConns(n int) Option {
	return func(c *config) { c.maxIdleConns = n }
}

// WithConnMaxLifetime sets the maximum lifetime of a connection.
func WithConnMaxLifetime(d time.Duration) Option {
	return func(c *config) { c.connMaxLifetime = d }
}

// WithConnMaxIdleTime sets the maximum idle time of a connection.
func WithConnMaxIdleTime(d time.Duration) Option {
	return func(c *config) { c.connMaxIdleTime = d }
}

// Open returns a [initd.Value]-compatible callback that opens a [sql.DB],
// configures the pool, registers a readiness probe, and hooks cleanup
// via [initd.Scope.OnExit].
func Open(opts ...Option) func(*initd.Scope) (*sql.DB, error) {
	return func(s *initd.Scope) (*sql.DB, error) {
		cfg := config{}
		for _, o := range opts {
			o(&cfg)
		}

		open := cfg.openFunc
		if open == nil {
			open = sql.Open
		}

		db, err := open(cfg.driver, cfg.dsn)
		if err != nil {
			return nil, err
		}

		if cfg.maxOpenConns > 0 {
			db.SetMaxOpenConns(cfg.maxOpenConns)
		}
		if cfg.maxIdleConns > 0 {
			db.SetMaxIdleConns(cfg.maxIdleConns)
		}
		if cfg.connMaxLifetime > 0 {
			db.SetConnMaxLifetime(cfg.connMaxLifetime)
		}
		if cfg.connMaxIdleTime > 0 {
			db.SetConnMaxIdleTime(cfg.connMaxIdleTime)
		}

		if err := db.PingContext(s.Context()); err != nil {
			db.Close()
			return nil, err
		}
		slog.InfoContext(s.Context(), "connected")

		s.Readiness(db.PingContext)

		s.OnExit(func(_ context.Context) error {
			return db.Close()
		})

		return db, nil
	}
}
