// Package httpapi exposes the Ogg Opus auditor over HTTP.
package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/acoustics/opus-audit/internal/audit"
	"github.com/acoustics/opus-audit/internal/errs"
)

// MaxBodyBytes is the 8 MiB cap on an uploaded logical stream.
const MaxBodyBytes = 8 << 20

// NewServer builds the HTTP mux with the audit and health endpoints.
func NewServer(logger *log.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", handleHealth)
	mux.HandleFunc("/api/opus/audit", func(w http.ResponseWriter, r *http.Request) {
		handleAudit(w, r, logger)
	})
	return logRequests(logger, mux)
}

func handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func handleAudit(w http.ResponseWriter, r *http.Request, logger *log.Logger) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", -1,
			"only POST is supported for /api/opus/audit")
		return
	}
	if mediaType(r.Header.Get("Content-Type")) != "audio/ogg" {
		writeError(w, http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA_TYPE", -1,
			"Content-Type must be audio/ogg")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, MaxBodyBytes)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", -1,
				"upload exceeds the 8 MiB limit")
			return
		}
		logger.Printf("read body: %v", err)
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", -1, "could not read request body")
		return
	}

	stats, aerr := audit.Audit(data)
	if aerr != nil {
		var ve *errs.Error
		if errors.As(aerr, &ve) {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": ve})
			return
		}
		logger.Printf("audit: %v", aerr)
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", -1, "audit failed")
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

// mediaType strips parameters (e.g. "audio/ogg; codecs=opus") and lowercases.
func mediaType(ct string) string {
	ct = strings.TrimSpace(ct)
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	return strings.ToLower(strings.TrimSpace(ct))
}

func writeError(w http.ResponseWriter, status int, code string, page int, msg string) {
	writeJSON(w, status, map[string]any{
		"error": errs.Error{Code: code, Page: page, Message: msg},
	})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	_ = enc.Encode(body)
}

// statusRecorder captures the response code for request logging.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// logRequests emits one structured-ish line per request.
func logRequests(logger *log.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(rec, r)
		logger.Printf("%s %s -> %d (%s)", r.Method, r.URL.Path, rec.status, time.Since(start))
	})
}
