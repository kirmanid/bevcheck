package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"Bevcheck/internal/model"
)

const (
	// imageDownloadTimeout is the overall ceiling for one fetch attempt: a
	// slow-but-alive host gets this long to finish a <=5 MiB image, while the
	// transport timeouts below fail dead hosts faster.
	imageDownloadTimeout = 30 * time.Second
	// maxJSONLBodyBytes caps the whole JSONL batch body.
	maxJSONLBodyBytes = 32 << 20
)

const (
	// downloadRetryBudget is the number of retries after the first attempt (so
	// up to 1 + downloadRetryBudget total requests) for transient failures.
	downloadRetryBudget = 3
	// downloadRetryMax caps a single backoff so a retry chain cannot stall a
	// batch item for more than a couple seconds.
	downloadRetryMax = 2 * time.Second
)

// downloadRetryBase is the initial backoff before the first retry; each
// subsequent retry doubles it (capped at downloadRetryMax). A var so tests can
// shrink it.
var downloadRetryBase = 500 * time.Millisecond

// imageHTTPClient is shared across all downloads: connection pooling keeps bulk
// batches from re-dialing the same host, and the granular timeouts bound dead
// or stalled servers without shrinking the overall budget.
var imageHTTPClient = &http.Client{
	Timeout: imageDownloadTimeout,
	Transport: &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
	},
}

// handleBatch accepts a JSONL body (one flat application + image_url per line),
// downloads each image, and verifies it through the same async job pipeline as
// /v1/verify. A line whose image cannot be fetched is reported as Needs
// Correction (needs attention), not a hard failure.
func handleBatch(ocr *OCRClient, store *jobStore, limiter *dailyLimiter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxJSONLBodyBytes)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "JSONL body too large or unreadable"})
			return
		}
		items, err := parseJSONL(body)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if len(items) > maxBatchDocuments {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("batch of %d documents exceeds per-request cap of %d", len(items), maxBatchDocuments)})
			return
		}
		if !limiter.allow(len(items)) {
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "daily document limit reached"})
			return
		}
		pairs := make([]pair, len(items))
		for i := range items {
			pairs[i] = pair{url: items[i].ImageURL, app: &items[i].Application}
		}
		id := store.submit(ocr, pairs)
		writeJSON(w, http.StatusAccepted, map[string]string{"job_id": id})
	}
}

// parseJSONL parses a JSONL body: one flat BatchItem per non-blank line.
func parseJSONL(body []byte) ([]model.BatchItem, error) {
	lines := bytes.Split(body, []byte("\n"))
	items := make([]model.BatchItem, 0, len(lines))
	for i, line := range lines {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var item model.BatchItem
		if err := json.Unmarshal(line, &item); err != nil {
			return nil, fmt.Errorf("line %d: %w", i+1, err)
		}
		item.ImageURL = strings.TrimSpace(item.ImageURL)
		if item.ImageURL == "" {
			return nil, fmt.Errorf("line %d: missing image_url", i+1)
		}
		if err := validateApplication(&item.Application); err != nil {
			return nil, fmt.Errorf("line %d: %w", i+1, err)
		}
		items = append(items, item)
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("empty JSONL body")
	}
	return items, nil
}

// downloadImage fetches a label image over HTTP(S), enforcing the 5 MiB and
// PNG/JPEG limits. Transient failures (a flaky CDN, a dropped connection) are
// retried once; permanent ones (4xx, non-image bytes) fail fast. Errors are
// returned to the caller, which reports them as Needs Correction rather than a
// hard failure.
func downloadImage(rawURL string) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid image_url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("unsupported image_url scheme %q (want http or https)", u.Scheme)
	}

	var b []byte
	for attempt := 0; ; attempt++ {
		b, err = downloadOnce(rawURL)
		if err == nil {
			break
		}
		if !transient(err) || attempt >= downloadRetryBudget {
			return nil, err
		}
		time.Sleep(backoffFor(attempt))
	}

	// Validate once, outside the retry loop: size/type failures are permanent.
	return b, validateImage(b)
}

// backoffFor returns the sleep before the next retry: exponential
// (downloadRetryBase << retry) capped at downloadRetryMax, with full jitter in
// [0, backoff].
func backoffFor(retry int) time.Duration {
	backoff := downloadRetryBase << retry
	if backoff > downloadRetryMax {
		backoff = downloadRetryMax
	}
	if backoff <= 0 {
		return 0
	}
	return time.Duration(rand.Int63n(int64(backoff) + 1))
}

// downloadOnce performs one GET and reads the (bounded) body. It returns only
// transport and status errors; size and type are validated by the caller.
func downloadOnce(rawURL string) ([]byte, error) {
	resp, err := imageHTTPClient.Get(rawURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &httpStatusError{code: resp.StatusCode}
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxImageBytes+1))
}

// httpStatusError carries a non-200 response status so transient() can retry
// 5xx/429/408 but not 4xx.
type httpStatusError struct{ code int }

func (e *httpStatusError) Error() string { return fmt.Sprintf("HTTP %d", e.code) }

// transient reports whether a fetch error is worth one retry: transport-level
// failures (timeout, DNS, reset, truncation) and the usual retryable codes.
func transient(err error) bool {
	var se *httpStatusError
	if errors.As(err, &se) {
		switch se.code {
		case 408, 429, 500, 502, 503, 504:
			return true
		}
		return false
	}
	return true
}

// isPNGOrJPEG sniffs magic bytes rather than trusting Content-Type, which
// object stores often serve as application/octet-stream regardless of the file.
func isPNGOrJPEG(b []byte) bool {
	if len(b) >= 8 && bytes.Equal(b[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}) {
		return true
	}
	return len(b) >= 3 && b[0] == 0xff && b[1] == 0xd8 && b[2] == 0xff
}

// validateImage enforces the shared image limits for the upload and download
// paths: size cap, non-empty, and PNG/JPEG magic bytes.
func validateImage(b []byte) error {
	if len(b) > maxImageBytes {
		return fmt.Errorf("image exceeds %d bytes", maxImageBytes)
	}
	if len(b) == 0 {
		return fmt.Errorf("empty image")
	}
	if !isPNGOrJPEG(b) {
		return fmt.Errorf("unsupported image type (want PNG or JPEG)")
	}
	return nil
}
