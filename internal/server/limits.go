package server

import (
	"fmt"
	"sync"
	"time"

	"Bevcheck/internal/model"
)

const (
	// dailyDocumentLimit caps the number of documents (label images) processed
	// per UTC calendar day. Set well above the ~410/day implied by the
	// 150k/year workload (research.txt 6.7.1) so batch demos are never blocked.
	dailyDocumentLimit = 2000
	// maxBatchDocuments caps a single request to bound in-memory buffering and
	// goroutine fan-out while staying well above the 200-300 peak batch.
	maxBatchDocuments = 1000
	// maxApplicationJSONBytes caps the application JSON per document (liberal;
	// the schema is a handful of short strings).
	maxApplicationJSONBytes = 1 << 20
	// maxConcurrentItems caps how many batch items run their download + OCR +
	// verify concurrently, bounding memory (one image per in-flight item) and
	// Textract request fan-out.
	maxConcurrentItems = 32
)

// dailyLimiter caps document throughput per UTC calendar day.
type dailyLimiter struct {
	mu    sync.Mutex
	day   int64
	count int
	limit int
}

func newDailyLimiter(limit int) *dailyLimiter {
	return &dailyLimiter{limit: limit}
}

// allow reports whether n more documents fit under the daily cap, consuming
// them if so. The counter resets when the UTC day changes.
func (l *dailyLimiter) allow(n int) bool {
	if n <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	day := time.Now().UTC().Unix() / 86400
	if day != l.day {
		l.day = day
		l.count = 0
	}
	if l.count+n > l.limit {
		return false
	}
	l.count += n
	return true
}

var validProductTypes = map[string]bool{
	"wine":              true,
	"distilled_spirits": true,
	"malt_beverages":    true,
}

var validSources = map[string]bool{
	"domestic": true,
	"imported": true,
}

// validateApplication rejects unknown enums at ingestion so an unrecognized
// product type cannot be silently graded as distilled spirits.
func validateApplication(a *model.Application) error {
	if !validProductTypes[a.ProductType] {
		return fmt.Errorf("invalid product_type %q (want wine, distilled_spirits, or malt_beverages)", a.ProductType)
	}
	if a.Source != "" && !validSources[a.Source] {
		return fmt.Errorf("invalid source %q (want domestic or imported)", a.Source)
	}
	return nil
}
