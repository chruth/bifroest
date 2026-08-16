// Package server wires up the HTTP surface of the application: the health
// endpoint and the webhook routes.
package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
)

// New builds the top-level HTTP handler. webhookHandler serves everything
// under /webhook/.
func New(webhookHandler http.Handler, mountAvailable func() bool, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", handleHealth)
	mux.HandleFunc("GET /ready", handleReady(mountAvailable))
	mux.Handle("POST /webhook/", webhookHandler)

	return withLogging(mux, log)
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleReady reports mount availability as an optional convenience for
// operators. Unlike /health, this can report degraded state, but it is
// never used to gate accepting webhooks.
func handleReady(mountAvailable func() bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		status := "ok"
		if !mountAvailable() {
			status = "mount_unavailable"
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": status})
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func withLogging(next http.Handler, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)

		// Health checks are typically polled every few seconds by Docker
		// or an orchestrator and carry no useful information in the
		// common (successful) case - logging them just drowns out
		// everything else.
		if r.URL.Path == "/health" || r.URL.Path == "/ready" {
			return
		}

		log.Info("http request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", sw.status,
			"duration", time.Since(start).String(),
		)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

// Serve runs the HTTP server until ctx is cancelled, then shuts it down
// gracefully.
func Serve(ctx context.Context, addr string, handler http.Handler, log *slog.Logger) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("http server listening", "address", addr)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			return err
		}
		return nil
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		log.Info("shutting down http server")
		return srv.Shutdown(shutdownCtx)
	}
}
