// Package logging provides a compact, human-readable slog.Handler for
// console output: a dim timestamp, a color-coded level, a bold message,
// and key=value attributes with a few semantic highlights (booleans,
// HTTP-status-like numbers, and "error" values). The stdlib TextHandler
// can't do this cleanly - it quote-escapes control characters, which
// breaks ANSI color codes fed through ReplaceAttr - so this implements
// slog.Handler directly instead.
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
	colorBold   = "\x1b[1m"
	colorDim    = "\x1b[2m"
	colorRed    = "\x1b[31m"
	colorGreen  = "\x1b[32m"
	colorYellow = "\x1b[33m"
	colorBlue   = "\x1b[34m"
	colorCyan   = "\x1b[36m"
)

// Handler is a slog.Handler for terminal/Docker-log output. Colors are on
// by default unless the NO_COLOR environment variable is set
// (https://no-color.org), or the Handler was built with NewPlain.
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

// NewPlain is like New but never emits ANSI color codes, regardless of
// NO_COLOR - for output that isn't a terminal, e.g. a log file, where
// escape codes would just be noise for whoever (or whatever tool) reads it
// later.
func NewPlain(w io.Writer, level slog.Level) *Handler {
	h := New(w, level)
	h.color = false
	return h
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

	h.writeColored(&buf, r.Message, colorBold)

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
	if h.color && color != "" {
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
	h.writeColored(buf, val, attrColor(key, v))
}

// attrColor picks a highlight for a few attribute shapes that are usually
// the first thing worth noticing at a glance in a wall of log lines:
//   - "error" values, regardless of the line's level (e.g. a retryable
//     failure logged at Warn still deserves to stand out)
//   - booleans (green true / dim false) - reads naturally for the startup
//     summary's "enabled=true" style lines
//   - "status", read as an HTTP status code when numeric (green 2xx,
//     blue 3xx, yellow 4xx, red 5xx)
//
// Anything else is left in the default color; returns "" for that case,
// which writeColored treats as "don't color this".
func attrColor(key string, v slog.Value) string {
	switch {
	case key == "error":
		return colorRed
	case key == "status":
		if c, ok := statusColor(v); ok {
			return c
		}
	case v.Kind() == slog.KindBool:
		if v.Bool() {
			return colorGreen
		}
		return colorDim
	}
	return ""
}

func statusColor(v slog.Value) (string, bool) {
	var code int64
	switch v.Kind() {
	case slog.KindInt64:
		code = v.Int64()
	case slog.KindUint64:
		code = int64(v.Uint64())
	default:
		return "", false
	}
	switch {
	case code >= 500:
		return colorRed, true
	case code >= 400:
		return colorYellow, true
	case code >= 300:
		return colorCyan, true
	case code >= 200:
		return colorGreen, true
	default:
		return "", false
	}
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
