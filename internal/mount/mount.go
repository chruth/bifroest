// Package mount watches the single global rclone anchor file and exposes
// whether the media mount is currently healthy.
package mount

import (
	"context"
	"log/slog"
	"os"
	"sync/atomic"
	"time"
)

// Checker reports whether the media mount is currently available.
type Checker interface {
	Available() bool
}

// Monitor periodically stats the anchor file and tracks mount availability.
// The zero value is not usable; construct with New.
type Monitor struct {
	anchor   string
	interval time.Duration
	log      *slog.Logger

	available atomic.Bool
}

func New(anchor string, interval time.Duration, log *slog.Logger) *Monitor {
	m := &Monitor{anchor: anchor, interval: interval, log: log}
	m.available.Store(statAnchor(anchor))
	return m
}

// Available reports the last-known mount state. Safe for concurrent use.
func (m *Monitor) Available() bool {
	return m.available.Load()
}

// Run polls the anchor file until ctx is cancelled, logging state
// transitions as they happen.
func (m *Monitor) Run(ctx context.Context) {
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.check()
		}
	}
}

func (m *Monitor) check() {
	nowAvailable := statAnchor(m.anchor)
	wasAvailable := m.available.Swap(nowAvailable)

	if nowAvailable && !wasAvailable {
		m.log.Info("mount available", "anchor", m.anchor)
	} else if !nowAvailable && wasAvailable {
		m.log.Warn("mount unavailable", "anchor", m.anchor)
	}
}

func statAnchor(anchor string) bool {
	_, err := os.Stat(anchor)
	return err == nil
}
