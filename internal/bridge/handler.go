package bridge

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/jleight/retroarch-romm-bridge/internal/mapping"
)

// maxUploadBytes caps a single PUT body. Save states are the largest payloads
// (a few MB); 64 MiB is comfortably above any real emulator save/state.
const maxUploadBytes = 64 << 20

// Handler implements the WebDAV façade RetroArch talks to.
type Handler struct {
	svc *Service
}

// NewHandler returns an http.Handler serving the WebDAV bridge.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// OPTIONS is a pre-auth capability probe; answer it without credentials.
	if r.Method == http.MethodOptions {
		h.handleOptions(w)
		return
	}

	b, ok := h.authenticate(r)
	if !ok {
		w.Header().Set("WWW-Authenticate", `Basic realm="retroarch-romm-bridge"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	switch r.Method {
	case http.MethodGet, http.MethodHead:
		h.handleGet(w, r, b)
	case http.MethodPut:
		h.handlePut(w, r, b)
	case "MKCOL", "MOVE", "DELETE", "COPY":
		// We don't model directories, and deletions are intentionally no-ops.
		// Acknowledge so RetroArch's sync proceeds.
		slog.Debug("no-op webdav verb", "method", r.Method, "path", r.URL.Path)
		w.WriteHeader(http.StatusCreated)
	case "PROPFIND":
		// RetroArch never issues PROPFIND, but answer benignly if probed.
		w.WriteHeader(http.StatusNotFound)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// authenticate resolves HTTP Basic credentials (username + RomM pair code) to a
// backend, exchanging the code for a token on first use.
func (h *Handler) authenticate(r *http.Request) (*userBackend, bool) {
	username, password, ok := r.BasicAuth()
	if !ok {
		return nil, false
	}
	b, err := h.svc.Resolve(r.Context(), username, password)
	if err != nil {
		if !errors.Is(err, ErrUnauthorized) {
			slog.Error("resolve credentials", "user", username, "err", err)
		}
		return nil, false
	}
	return b, true
}

func (h *Handler) handleOptions(w http.ResponseWriter) {
	w.Header().Set("DAV", "1, 2")
	w.Header().Set("Allow", "OPTIONS, GET, HEAD, PUT, DELETE, MKCOL, MOVE, COPY, PROPFIND")
	w.Header().Set("MS-Author-Via", "DAV")
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) handleGet(w http.ResponseWriter, r *http.Request, b *userBackend) {
	result, key := mapping.Parse(r.URL.Path)
	switch result {
	case mapping.ResultManifest:
		h.serveManifest(w, r, b)
	case mapping.ResultAsset:
		h.serveAsset(w, r, b, key)
	default:
		http.NotFound(w, r)
	}
}

func (h *Handler) serveManifest(w http.ResponseWriter, r *http.Request, b *userBackend) {
	entries, err := h.svc.Manifest(r.Context(), b)
	if err != nil {
		slog.Error("build manifest", "token", b.shortToken(), "err", err)
		http.Error(w, "manifest error", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodHead {
		return
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(entries); err != nil {
		slog.Error("encode manifest", "token", b.shortToken(), "err", err)
	}
}

func (h *Handler) serveAsset(w http.ResponseWriter, r *http.Request, b *userBackend, key mapping.Key) {
	body, err := h.svc.Download(r.Context(), b, key)
	if err != nil {
		slog.Error("download asset", "token", b.shortToken(), "path", r.URL.Path, "err", err)
		http.Error(w, "download error", http.StatusBadGateway)
		return
	}
	if body == nil {
		http.NotFound(w, r) // RetroArch treats 404 as "file absent" — valid.
		return
	}
	defer body.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	if r.Method == http.MethodHead {
		return
	}
	if _, err := io.Copy(w, body); err != nil {
		slog.Warn("stream asset", "token", b.shortToken(), "path", r.URL.Path, "err", err)
	}
}

func (h *Handler) handlePut(w http.ResponseWriter, r *http.Request, b *userBackend) {
	result, key := mapping.Parse(r.URL.Path)
	if result == mapping.ResultManifest {
		// RetroArch uploads its view of the server manifest; we generate the
		// manifest dynamically, so accept and discard.
		_, _ = io.Copy(io.Discard, http.MaxBytesReader(w, r.Body, maxUploadBytes))
		w.WriteHeader(http.StatusCreated)
		return
	}
	if result != mapping.ResultAsset {
		http.NotFound(w, r)
		return
	}

	content, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxUploadBytes))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}

	if err := h.svc.Store(r.Context(), b, key, content); err != nil {
		if IsUnmapped(err) {
			// Non-fatal for RetroArch: it logs the failed file and continues.
			slog.Warn("upload skipped: no matching rom", "token", b.shortToken(), "path", r.URL.Path, "err", err)
			http.Error(w, "no matching rom in RomM", http.StatusNotFound)
			return
		}
		slog.Error("store upload", "token", b.shortToken(), "path", r.URL.Path, "err", err)
		http.Error(w, "store error", http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusCreated)
}
