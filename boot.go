package initd

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/struct0x/envconfig"
)

// Boot is a minimal bootstrap handle created by [Minimal].
// It provides a context and loaded config, but no lifecycle management.
// Pass it to [WithBoot] when creating the full [App].
// Register cleanup hooks with [Boot.OnHandoff] — they run when [App.Run]
// starts, signaling the end of the bootstrap phase.
type Boot struct {
	Logger *slog.Logger

	ctx      context.Context
	cancel   context.CancelFunc
	closeFns []func()
}

// Context returns the bootstrap context. It is canceled when
// [App.Run] starts, signaling the end of the bootstrap phase.
func (b *Boot) Context() context.Context {
	return b.ctx
}

// OnHandoff registers a function to run when the bootstrap phase ends
// (i.e. when [App.Run] is called). Use this to close temporary
// resources that were only needed during config loading.
func (b *Boot) OnHandoff(fn func()) {
	b.closeFns = append(b.closeFns, fn)
}

// close cancels the boot context and runs all OnHandoff hooks.
func (b *Boot) close() {
	b.cancel()
	for _, fn := range b.closeFns {
		fn()
	}
}

// Minimal loads config from environment variables and returns a [Boot]
// handle. Use this when dependencies needed during config loading
// (e.g. a secret store client) must exist before [New].
func Minimal[C any](cfg *C, opts ...Option) (*Boot, error) {
	ac := appConfig{}
	for _, o := range opts {
		o(&ac)
	}

	if !ac.skipEnvLoad {
		lookups := ac.envLookups
		if len(lookups) == 0 {
			lookups = []envconfig.LookupEnv{os.LookupEnv}
		}
		if err := envconfig.Read(cfg, lookups...); err != nil {
			return nil, fmt.Errorf("initd: %w", err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	return &Boot{
		Logger: newLogger(&ac).WithGroup("boot"),
		ctx:    ctx,
		cancel: cancel,
	}, nil
}
