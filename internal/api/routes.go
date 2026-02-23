package api

import (
	"log/slog"
	"net/http"
	"runtime/debug"
)

// NewRouter creates the HTTP mux with all API routes.
func NewRouter(h *Handler) *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /v1/machines", h.CreateMachine)
	mux.HandleFunc("GET /v1/machines", h.ListMachines)
	mux.HandleFunc("GET /v1/machines/{id}", h.GetMachine)
	mux.HandleFunc("DELETE /v1/machines/{id}", h.DeleteMachine)
	mux.HandleFunc("POST /v1/machines/{id}/start", h.StartMachine)
	mux.HandleFunc("POST /v1/machines/{id}/stop", h.StopMachine)
	mux.HandleFunc("GET /v1/machines/{id}/logs", h.GetLogs)

	return mux
}

// WithMiddleware wraps a handler with logging, recovery, and content-type middleware.
func WithMiddleware(h http.Handler) http.Handler {
	return recoverer(logger(setContentType(h)))
}

func setContentType(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		next.ServeHTTP(w, r)
	})
}

func logger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slog.Info("request", "method", r.Method, "path", r.URL.Path, "remote", r.RemoteAddr)
		next.ServeHTTP(w, r)
	})
}

func recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				slog.Error("panic recovered", "error", err, "stack", string(debug.Stack()))
				http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}
