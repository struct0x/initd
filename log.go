package initd

import (
	"context"
	"log/slog"
)

type componentKey struct{}

// withComponent returns a copy of ctx carrying the component name.
// Log records emitted with [slog.InfoContext](ctx, ...) will automatically
// include a "component" attribute.
func withComponent(ctx context.Context, name string) context.Context {
	return context.WithValue(ctx, componentKey{}, name)
}

type componentHandler struct {
	inner        slog.Handler
	hasComponent bool
}

func (h *componentHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *componentHandler) Handle(ctx context.Context, r slog.Record) error {
	if !h.hasComponent {
		if name, ok := ctx.Value(componentKey{}).(string); ok {
			r.AddAttrs(slog.String("component", name))
		}
	}
	return h.inner.Handle(ctx, r)
}

func (h *componentHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	has := h.hasComponent
	for _, a := range attrs {
		if a.Key == "component" {
			has = true
			break
		}
	}
	return &componentHandler{inner: h.inner.WithAttrs(attrs), hasComponent: has}
}

func (h *componentHandler) WithGroup(name string) slog.Handler {
	return &componentHandler{inner: h.inner.WithGroup(name), hasComponent: h.hasComponent}
}

type durationHandler struct {
	inner slog.Handler
}

func (h *durationHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *durationHandler) Handle(ctx context.Context, r slog.Record) error {
	var attrs []slog.Attr
	r.Attrs(func(a slog.Attr) bool {
		attrs = append(attrs, convertDurationAttr(a))
		return true
	})

	nr := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	nr.AddAttrs(attrs...)
	return h.inner.Handle(ctx, nr)
}

func (h *durationHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	converted := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		converted[i] = convertDurationAttr(a)
	}
	return &durationHandler{inner: h.inner.WithAttrs(converted)}
}

func (h *durationHandler) WithGroup(name string) slog.Handler {
	return &durationHandler{inner: h.inner.WithGroup(name)}
}

func convertDurationAttr(a slog.Attr) slog.Attr {
	switch a.Value.Kind() {
	case slog.KindDuration:
		return slog.String(a.Key, a.Value.Duration().String())
	case slog.KindGroup:
		ga := a.Value.Group()
		converted := make([]slog.Attr, len(ga))
		for i, g := range ga {
			converted[i] = convertDurationAttr(g)
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(converted...)}
	default:
		return a
	}
}
