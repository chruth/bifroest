package logging

import (
	"context"
	"errors"
	"log/slog"
)

// FanOut dispatches every record to multiple slog.Handlers - used to log
// to stdout (colored) and a file (plain) at once.
type FanOut struct {
	handlers []slog.Handler
}

// NewFanOut combines handlers into one. Enabled reports true if any
// handler would accept the level, so Handle only skips a handler that
// individually filters it out.
func NewFanOut(handlers ...slog.Handler) *FanOut {
	return &FanOut{handlers: handlers}
}

func (f *FanOut) Enabled(ctx context.Context, level slog.Level) bool {
	for _, h := range f.handlers {
		if h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (f *FanOut) Handle(ctx context.Context, r slog.Record) error {
	var errs []error
	for _, h := range f.handlers {
		if !h.Enabled(ctx, r.Level) {
			continue
		}
		// Record.Attrs can only be iterated once; each handler needs its
		// own independent copy to walk.
		if err := h.Handle(ctx, r.Clone()); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (f *FanOut) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := make([]slog.Handler, len(f.handlers))
	for i, h := range f.handlers {
		next[i] = h.WithAttrs(attrs)
	}
	return &FanOut{handlers: next}
}

func (f *FanOut) WithGroup(name string) slog.Handler {
	next := make([]slog.Handler, len(f.handlers))
	for i, h := range f.handlers {
		next[i] = h.WithGroup(name)
	}
	return &FanOut{handlers: next}
}
