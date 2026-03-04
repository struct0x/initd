package initd

import (
	"context"
	"sync"
	"time"
)

// note: it's set to 9 seconds, to be ahead of the default k8s 10s check period.
const defaultProbeInterval = 9 * time.Second

type probeKind int

const (
	readinessProbe probeKind = iota
	livenessProbe
	startupProbe
)

// ProbeOption configures probe behavior.
type ProbeOption func(*probeConfig)

type probeConfig struct {
	timeout      time.Duration
	interval     time.Duration
	failAfter    int
	initialDelay time.Duration
	oneShot      bool
}

// ProbeTimeout sets the per-evaluation context timeout.
func ProbeTimeout(d time.Duration) ProbeOption {
	return func(c *probeConfig) { c.timeout = d }
}

// ProbeInterval sets how often the probe is evaluated in the background.
// Default is 9s.
func ProbeInterval(d time.Duration) ProbeOption {
	return func(c *probeConfig) { c.interval = d }
}

// ProbeFailAfter marks the probe unhealthy only after n consecutive failures.
func ProbeFailAfter(n int) ProbeOption {
	return func(c *probeConfig) { c.failAfter = n }
}

// ProbeInitialDelay waits before starting periodic evaluation.
func ProbeInitialDelay(d time.Duration) ProbeOption {
	return func(c *probeConfig) { c.initialDelay = d }
}

func probeOneShot() ProbeOption {
	return func(c *probeConfig) { c.oneShot = true }
}

// ProbeResult is the aggregate result of all probes of a kind.
type ProbeResult struct {
	Healthy bool
	Checks  map[string]CheckResult
}

// CheckResult is the last-known result of a single probe.
type CheckResult struct {
	Healthy  bool
	Duration time.Duration
	Error    string
}

type probe struct {
	name   string
	fn     func(context.Context) error
	config probeConfig

	mu               sync.RWMutex
	lastResult       CheckResult
	consecutiveFails int
}

func newProbe(name string, fn func(context.Context) error, opts ...ProbeOption) *probe {
	var cfg probeConfig
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.interval == 0 {
		cfg.interval = defaultProbeInterval
	}
	return &probe{
		name:       name,
		fn:         fn,
		config:     cfg,
		lastResult: CheckResult{Healthy: false},
	}
}

func (p *probe) run(ctx context.Context) {
	if p.config.initialDelay > 0 {
		select {
		case <-time.After(p.config.initialDelay):
		case <-ctx.Done():
			return
		}
	}

	if p.evaluate(ctx) && p.config.oneShot {
		return
	}

	ticker := time.NewTicker(p.config.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			p.evaluate(ctx)
		case <-ctx.Done():
			p.evaluateDone()
			return
		}
	}
}

func (p *probe) evaluate(ctx context.Context) (healthy bool) {
	if p.config.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, p.config.timeout)
		defer cancel()
	}

	start := time.Now()
	err := p.fn(ctx)
	d := time.Since(start)

	p.mu.Lock()
	defer p.mu.Unlock()

	if err != nil {
		p.consecutiveFails++
		result := CheckResult{Duration: d, Error: err.Error()}
		if p.config.failAfter > 0 && p.consecutiveFails < p.config.failAfter {
			result.Healthy = p.lastResult.Healthy
		}
		p.lastResult = result
		return p.lastResult.Healthy
	}

	p.consecutiveFails = 0
	p.lastResult = CheckResult{Healthy: true, Duration: d}

	return p.lastResult.Healthy
}

func (p *probe) evaluateDone() {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.lastResult = CheckResult{Healthy: false}
}

func (p *probe) result() CheckResult {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.lastResult
}

type probeRegistry struct {
	mu        sync.RWMutex
	ctx       context.Context
	readiness []*probe
	liveness  []*probe
	startup   []*probe
}

func newProbeRegistry(ctx context.Context) *probeRegistry {
	return &probeRegistry{ctx: ctx}
}

func (r *probeRegistry) register(kind probeKind, name string, fn func(context.Context) error, opts ...ProbeOption) {
	p := newProbe(name, fn, opts...)

	r.mu.Lock()
	switch kind {
	case readinessProbe:
		r.readiness = append(r.readiness, p)
	case livenessProbe:
		r.liveness = append(r.liveness, p)
	case startupProbe:
		r.startup = append(r.startup, p)
	}
	r.mu.Unlock()

	go p.run(r.ctx)
}

func (r *probeRegistry) check(kind probeKind) ProbeResult {
	r.mu.RLock()
	var probes []*probe
	switch kind {
	case readinessProbe:
		probes = r.readiness
	case livenessProbe:
		probes = r.liveness
	case startupProbe:
		probes = r.startup
	}
	r.mu.RUnlock()

	result := ProbeResult{
		Healthy: true,
		Checks:  make(map[string]CheckResult, len(probes)),
	}

	for _, p := range probes {
		cr := p.result()
		result.Checks[p.name] = cr
		if !cr.Healthy {
			result.Healthy = false
		}
	}

	return result
}
