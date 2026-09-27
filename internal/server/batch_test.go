package server

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseJSONL(t *testing.T) {
	valid := `{"product_type":"distilled_spirits","source":"domestic","brand_name":"OLD TOM","image_url":"http://x/l.png"}`
	items, err := parseJSONL([]byte(valid))
	if err != nil {
		t.Fatalf("valid line: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len = %d, want 1", len(items))
	}
	if items[0].ImageURL != "http://x/l.png" || items[0].BrandName != "OLD TOM" || items[0].ProductType != "distilled_spirits" {
		t.Fatalf("unexpected item: %+v", items[0])
	}

	// A blank line between two valid lines is skipped, not an error.
	two := valid + "\n\n" + `{"product_type":"wine","brand_name":"X","image_url":"http://x/2.png"}`
	items, err = parseJSONL([]byte(two))
	if err != nil {
		t.Fatalf("blank line should be skipped: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("len = %d, want 2", len(items))
	}

	// Missing image_url.
	if _, err := parseJSONL([]byte(`{"product_type":"wine","brand_name":"X"}`)); err == nil {
		t.Fatalf("missing image_url should error")
	}

	// Invalid JSON.
	if _, err := parseJSONL([]byte("not json")); err == nil {
		t.Fatalf("invalid JSON should error")
	}

	// Invalid product_type enum.
	if _, err := parseJSONL([]byte(`{"product_type":"beer","image_url":"http://x"}`)); err == nil {
		t.Fatalf("invalid product_type should error")
	}

	// Blank-only body.
	if _, err := parseJSONL([]byte("\n\n")); err == nil {
		t.Fatalf("empty body should error")
	}
}

func TestDownloadImage(t *testing.T) {
	// Valid PNG (full 8-byte signature).
	pngSig := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}
	png := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngSig)
	}))
	defer png.Close()
	b, err := downloadImage(png.URL)
	if err != nil {
		t.Fatalf("png download: %v", err)
	}
	if len(b) != len(pngSig) {
		t.Fatalf("len = %d, want %d", len(b), len(pngSig))
	}

	// PNG bytes served as application/octet-stream (object stores do this):
	// magic-byte sniffing must accept it.
	octet := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(pngSig)
	}))
	defer octet.Close()
	if _, err := downloadImage(octet.URL); err != nil {
		t.Fatalf("png via octet-stream should be accepted: %v", err)
	}

	// Valid JPEG with a charset parameter.
	jpeg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg; charset=utf-8")
		_, _ = w.Write([]byte{0xff, 0xd8, 0xff})
	}))
	defer jpeg.Close()
	if _, err := downloadImage(jpeg.URL); err != nil {
		t.Fatalf("jpeg download: %v", err)
	}

	// Wrong content type.
	wrong := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html>"))
	}))
	defer wrong.Close()
	if _, err := downloadImage(wrong.URL); err == nil {
		t.Fatalf("text/html should be rejected")
	}

	// HTTP 404.
	notFound := httptest.NewServer(http.NotFoundHandler())
	defer notFound.Close()
	if _, err := downloadImage(notFound.URL); err == nil {
		t.Fatalf("404 should error")
	}

	// Empty image.
	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
	}))
	defer empty.Close()
	if _, err := downloadImage(empty.URL); err == nil {
		t.Fatalf("empty image should error")
	}

	// Oversized image.
	big := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(make([]byte, maxImageBytes+1))
	}))
	defer big.Close()
	if _, err := downloadImage(big.URL); err == nil {
		t.Fatalf("oversized image should be rejected")
	}
}

func TestDownloadImageRetriesTransient(t *testing.T) {
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&n, 1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
	}))
	defer srv.Close()
	b, err := downloadImage(srv.URL)
	if err != nil {
		t.Fatalf("retry should succeed: %v", err)
	}
	if len(b) != 8 {
		t.Fatalf("len = %d, want 8", len(b))
	}
	if atomic.LoadInt32(&n) != 2 {
		t.Fatalf("requests = %d, want 2", n)
	}
}

func TestDownloadImageRetryBudget(t *testing.T) {
	old := downloadRetryBase
	downloadRetryBase = time.Millisecond // keep the test fast; jitter sleeps are tiny
	defer func() { downloadRetryBase = old }()

	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&n, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	if _, err := downloadImage(srv.URL); err == nil {
		t.Fatalf("persistent 503 should error after exhausting retries")
	}
	if got := atomic.LoadInt32(&n); got != 1+downloadRetryBudget {
		t.Fatalf("requests = %d, want %d", got, 1+downloadRetryBudget)
	}
}

func TestBackoffForBounds(t *testing.T) {
	for retry := 0; retry <= 5; retry++ {
		cap := downloadRetryBase << retry
		if cap > downloadRetryMax {
			cap = downloadRetryMax
		}
		got := backoffFor(retry)
		if got < 0 || got > cap {
			t.Fatalf("backoffFor(%d) = %v, want in [0, %v]", retry, got, cap)
		}
	}
}

func TestDownloadImageNoRetryOnPermanent(t *testing.T) {
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&n, 1)
		http.NotFound(w, r)
	}))
	defer srv.Close()
	if _, err := downloadImage(srv.URL); err == nil {
		t.Fatalf("404 should error")
	}
	if atomic.LoadInt32(&n) != 1 {
		t.Fatalf("404 should not retry: requests = %d, want 1", n)
	}
}

func TestDownloadImageSchemeCheck(t *testing.T) {
	if _, err := downloadImage("ftp://host/label.png"); err == nil {
		t.Fatalf("ftp scheme should be rejected")
	}
	if _, err := downloadImage("not-a-url"); err == nil {
		t.Fatalf("scheme-less URL should be rejected")
	}
}

func TestItemNeedsCorrection(t *testing.T) {
	it := &Item{Status: jobRunning}
	it.needsCorrection("image download: context deadline exceeded")
	r := it.result()
	if r.Status != jobDone || r.Overall != "Needs Correction" {
		t.Fatalf("result = %+v, want done + Needs Correction", r)
	}
	if len(r.Criteria) != 1 {
		t.Fatalf("criteria len = %d, want 1", len(r.Criteria))
	}
	if r.Criteria[0].Name != "image_download" || r.Criteria[0].Status != statusNeedsCorrection {
		t.Fatalf("criterion = %+v", r.Criteria[0])
	}
}
