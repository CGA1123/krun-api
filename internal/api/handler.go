package api

import (
	"encoding/json"
	"io"
	"net/http"
	"os"

	"github.com/CGA1123/krun-api/machine"
)

// Handler holds the dependencies for HTTP handlers.
type Handler struct {
	mgr        *machine.Manager
	imageCache *ImageCache // nil if image support not configured
}

// NewHandler creates a new API handler.
func NewHandler(mgr *machine.Manager, imageCache *ImageCache) *Handler {
	return &Handler{mgr: mgr, imageCache: imageCache}
}

// CreateMachine handles POST /v1/machines.
func (h *Handler) CreateMachine(w http.ResponseWriter, r *http.Request) {
	var req CreateMachineRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	if req.BaseImage == "" {
		writeError(w, http.StatusBadRequest, "base_image is required")
		return
	}
	if req.Config.RootfsPath != "" {
		writeError(w, http.StatusBadRequest, "rootfs_path is not supported; use base_image")
		return
	}

	if req.Config.VCPUs == 0 {
		req.Config.VCPUs = 1
	}
	if req.Config.MemoryMiB == 0 {
		req.Config.MemoryMiB = 256
	}

	if h.imageCache == nil {
		writeError(w, http.StatusInternalServerError, "image support not configured (missing --vminit-path)")
		return
	}

	baseImagePath, err := h.imageCache.Ensure(r.Context(), req.BaseImage)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "extract image: "+err.Error())
		return
	}

	m, err := h.mgr.Create(req.Name, baseImagePath, req.Config, req.Network)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, m)
}

// ListMachines handles GET /v1/machines.
func (h *Handler) ListMachines(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.mgr.List())
}

// GetMachine handles GET /v1/machines/{id}.
func (h *Handler) GetMachine(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	m, err := h.mgr.Get(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, m)
}

// DeleteMachine handles DELETE /v1/machines/{id}.
// Pass ?delete_rootfs=true to also remove a cloned rootfs directory.
func (h *Handler) DeleteMachine(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	deleteRootfs := r.URL.Query().Get("delete_rootfs") == "true"
	if err := h.mgr.Delete(id, deleteRootfs); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// StartMachine handles POST /v1/machines/{id}/start.
func (h *Handler) StartMachine(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.mgr.Start(id); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	m, _ := h.mgr.Get(id)
	writeJSON(w, http.StatusOK, m)
}

// StopMachine handles POST /v1/machines/{id}/stop.
func (h *Handler) StopMachine(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.mgr.Stop(id); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	m, _ := h.mgr.Get(id)
	writeJSON(w, http.StatusOK, m)
}

// GetLogs handles GET /v1/machines/{id}/logs.
func (h *Handler) GetLogs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := h.mgr.Get(id); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	consolePath := h.mgr.ConsolePath(id)
	f, err := os.Open(consolePath)
	if err != nil {
		writeError(w, http.StatusNotFound, "no console output available")
		return
	}
	defer f.Close()

	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	io.Copy(w, f)
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, ErrorResponse{Error: msg})
}
