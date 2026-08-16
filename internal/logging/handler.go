// Package logging provides a compact, human-readable slog.Handler for
// console output: a dim timestamp, a color-coded level, the message, and
// key=value attributes. The stdlib TextHandler can't do this cleanly - it
// quote-escapes control characters, which breaks ANSI color codes fed
// through ReplaceAttr - so this implements slog.Handler directly instead.
package logging

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
)

const (
	colorReset  = "\x1b[0m"
	colorDim    = "\x1b[2m"
	colorRed    = "\x1b[31m"
	colorGreen  = "\x1b[32m"
	colorYellow = "\x1b[33m"
	colorBlue   = "\x1b[34m"
)

// Handler is a slog.Handler for terminal/Docker-log output. Colors are
// always on unless the NO_COLOR environment variable is set
// (https://no-color.org).
type Handler struct {
	mu    *sync.Mutex
	w     io.Writer
	level slog.Level
	color bool

	attrs       []slog.Attr // pre-formatted (fully-qualified) via WithAttrs
	groupPrefix string      // dotted prefix from WithGroup, applied to record attrs
}

// New builds a Handler writing to w at the given minimum level.
func New(w io.Writer, level slog.Level) *Handler {
	return &Handler{
		mu:    &sync.Mutex{},
		w:     w,
		level: level,
		color: os.Getenv("NO_COLOR") == "",
	}
}

func (h *Handler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level
}

func (h *Handler) Handle(_ context.Context, r slog.Record) error {
	var buf bytes.Buffer

	h.writeColored(&buf, r.Time.Format("2006-01-02 15:04:05"), colorDim)
	buf.WriteByte(' ')

	text, color := levelText(r.Level)
	h.writeColored(&buf, text, color)
	buf.WriteByte(' ')

	buf.WriteString(r.Message)

	for _, a := range h.attrs {
		h.writeAttr(&buf, a.Key, a.Value)
	}
	r.Attrs(func(a slog.Attr) bool {
		key := a.Key
		if h.groupPrefix != "" {
			key = h.groupPrefix + "." + key
		}
		h.writeAttr(&buf, key, a.Value)
		return true
	})

	buf.WriteByte('\n')

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := h.w.Write(buf.Bytes())
	return err
}

func (h *Handler) writeColored(buf *bytes.Buffer, s, color string) {
	if h.color {
		buf.WriteString(color)
		buf.WriteString(s)
		buf.WriteString(colorReset)
		return
	}
	buf.WriteString(s)
}

func (h *Handler) writeAttr(buf *bytes.Buffer, key string, v slog.Value) {
	buf.WriteByte(' ')
	h.writeColored(buf, key, colorDim)
	buf.WriteByte('=')

	val := quoteIfNeeded(v.String())
	// "error" is usually the one thing worth catching at a glance
	// regardless of level (e.g. a Warn logging a retryable failure), so
	// it's always highlighted red rather than only when the whole line is
	// already an Error-level line.
	if key == "error" {
		h.writeColored(buf, val, colorRed)
		return
	}
	buf.WriteString(val)
}

func quoteIfNeeded(s string) string {
	if s == "" || strings.ContainsAny(s, " \t\"=") {
		return strconv.Quote(s)
	}
	return s
}

func levelText(l slog.Level) (text, color string) {
	switch {
	case l < slog.LevelInfo:
		return "DEBUG", colorBlue
	case l < slog.LevelWarn:
		return "INFO ", colorGreen
	case l < slog.LevelError:
		return "WARN ", colorYellow
	default:
		return "ERROR", colorRed
	}
}

func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	newAttrs := make([]slog.Attr, 0, len(h.attrs)+len(attrs))
	newAttrs = append(newAttrs, h.attrs...)
	for _, a := range attrs {
		if h.groupPrefix != "" {
			a = slog.Attr{Key: h.groupPrefix + "." + a.Key, Value: a.Value}
		}
		newAttrs = append(newAttrs, a)
	}
	return &Handler{
		mu: h.mu, w: h.w, level: h.level, color: h.color,
		attrs: newAttrs, groupPrefix: h.groupPrefix,
	}
}

func (h *Handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	prefix := name
	if h.groupPrefix != "" {
		prefix = h.groupPrefix + "." + name
	}
	return &Handler{
		mu: h.mu, w: h.w, level: h.level, color: h.color,
		attrs: h.attrs, groupPrefix: prefix,
	}
}
