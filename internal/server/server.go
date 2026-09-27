// Package server wires the Bevcheck HTTP API: Textract OCR, label verification,
// async job polling, and rate limiting. The monolith entry point is cmd/Bevcheck.
//
// HTTP API (no auth yet):
//
//	GET  /                -> embedded web UI (index.html)
//	GET  /healthz         -> {"status":"ok"}
//	POST /v1/ocr          -> raw image bytes; returns Textract OCR (sync)
//	POST /v1/verify       -> 1..N images + matching applications; returns {job_id}
//	POST /v1/batch        -> JSONL: flat application + image_url per line; returns {job_id}
//	GET  /v1/jobs/{id}    -> poll a job (items + per-item criteria/overall)
//
// Single and batch use the same path: one image + one "application" object is
// a single; N images + an "applications" array is a batch. Both poll the same
// /v1/jobs/{id} endpoint.
package server

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"Bevcheck/internal/model"
)

const (
	defaultPort   = "80"
	maxImageBytes = 5 << 20 // 5 MiB (COLAs contract is 1.5 MiB)

	// maxMultipartBytes caps the whole /v1/verify multipart body: the largest
	// legitimate batch (maxBatchDocuments x maxImageBytes) plus slack for the
	// application JSON and multipart framing, so a hostile upload cannot spool
	// an unbounded amount to temp files.
	maxMultipartBytes = maxBatchDocuments*maxImageBytes + (64 << 20)
)

//go:embed index.html
var indexHTML []byte

// Run starts the HTTP server and blocks until a shutdown signal is received.
func Run() {
	port := envOr("PORT", defaultPort)
	region := envOr("AWS_REGION", "us-east-1")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ocr, err := NewOCRClient(ctx, region)
	if err != nil {
		log.Fatalf("ocr client: %v", err)
	}
	store := newJobStore(ctx)
	limiter := newDailyLimiter(dailyDocumentLimit)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handleHealth)
	mux.HandleFunc("GET /", handleIndex)
	mux.HandleFunc("POST /v1/ocr", handleOCR(ocr, limiter))
	mux.HandleFunc("POST /v1/verify", handleVerify(ocr, store, limiter))
	mux.HandleFunc("POST /v1/batch", handleBatch(ocr, store, limiter))
	mux.HandleFunc("GET /v1/jobs/{id}", handleGetJob(store))

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Printf("Bevcheck listening on :%s (region %s)", port, region)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		log.Fatalf("listen: %v", err)
	case <-ctx.Done():
		log.Printf("shutting down...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("graceful shutdown failed: %v", err)
		}
	}
}

func handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": "Bevcheck"})
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(indexHTML)
}

// ---------------------------------------------------------------------------
// OCR (sync)
// ---------------------------------------------------------------------------

func handleOCR(ocr *OCRClient, limiter *dailyLimiter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !limiter.allow(1) {
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "daily document limit reached"})
			return
		}
		img, ok := readBody(w, r)
		if !ok {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		res, err := ocr.Extract(ctx, img)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, res)
	}
}

// ---------------------------------------------------------------------------
// verify (unified single/batch, async)
// ---------------------------------------------------------------------------

func handleVerify(ocr *OCRClient, store *jobStore, limiter *dailyLimiter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxMultipartBytes)
		pairs, ok := parseSubmission(w, r)
		if !ok {
			return
		}
		if !limiter.allow(len(pairs)) {
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "daily document limit reached"})
			return
		}
		id := store.submit(ocr, pairs)
		writeJSON(w, http.StatusAccepted, map[string]string{"job_id": id})
	}
}

func handleGetJob(store *jobStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		j, ok := store.getJob(r.PathValue("id"))
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "job not found"})
			return
		}
		writeJSON(w, http.StatusOK, j.result())
	}
}

// parseSubmission accepts 1..N images plus matching application(s):
//   - single: one "image" file + "application" (JSON object)
//   - batch:  N "image" files + "applications" (JSON array)
func parseSubmission(w http.ResponseWriter, r *http.Request) ([]pair, bool) {
	if err := r.ParseMultipartForm(maxImageBytes); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "multipart parse: " + err.Error()})
		return nil, false
	}
	files := r.MultipartForm.File["image"]
	if len(files) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no 'image' files"})
		return nil, false
	}
	if len(files) > maxBatchDocuments {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("batch of %d documents exceeds per-request cap of %d", len(files), maxBatchDocuments)})
		return nil, false
	}

	var apps []model.Application
	raw := r.FormValue("applications")
	isBatch := raw != ""
	if !isBatch {
		raw = r.FormValue("application")
	}
	if raw == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing 'application' or 'applications'"})
		return nil, false
	}
	// Per-document JSON cap: the raw application JSON must not exceed
	// len(files) * maxApplicationJSONBytes.
	if int64(len(raw)) > int64(len(files))*maxApplicationJSONBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "application JSON too large"})
		return nil, false
	}
	if isBatch {
		if err := json.Unmarshal([]byte(raw), &apps); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid 'applications' JSON array: " + err.Error()})
			return nil, false
		}
	} else {
		var a model.Application
		if err := json.Unmarshal([]byte(raw), &a); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid 'application' JSON: " + err.Error()})
			return nil, false
		}
		apps = []model.Application{a}
	}

	if len(apps) != len(files) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "application count must match image count"})
		return nil, false
	}

	for i := range apps {
		if err := validateApplication(&apps[i]); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return nil, false
		}
	}

	pairs := make([]pair, len(files))
	for i, fh := range files {
		img, err := readUpload(fh)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return nil, false
		}
		pairs[i] = pair{img: img, app: &apps[i]}
	}
	return pairs, true
}

func readUpload(fh *multipart.FileHeader) ([]byte, error) {
	f, err := fh.Open()
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxImageBytes+1))
	if err != nil {
		return nil, err
	}
	return b, validateImage(b)
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxImageBytes)
	b, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "image too large or unreadable"})
		return nil, false
	}
	if len(b) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "empty request body"})
		return nil, false
	}
	return b, true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("write response: %v", err)
	}
}
