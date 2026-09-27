package server

import (
	"container/list"
	"sync"
)

const (
	// ocrCacheMaxEntries bounds the cache by count: the 4096 most-recently-used
	// images. This is the binding limit in practice — a label's extracted text
	// is a few KB, so 4096 entries is tens of MB, well under the byte cap.
	ocrCacheMaxEntries = 4096
	// ocrCacheMaxBytes is a hard byte ceiling (256 MiB) as a safety backstop.
	ocrCacheMaxBytes = 256 << 20
)

// ocrKey is the SHA-256 of the raw image bytes handed to Textract.
type ocrKey [32]byte

type ocrCacheEntry struct {
	key  ocrKey
	res  *OCRResult
	size int64
}

// ocrCache is an in-memory, fixed-size LRU of Textract results keyed by image
// hash. It avoids re-billing Textract when the same image is verified more than
// once (an agent or human re-submitting the same label). Bounded by both entry
// count and total bytes; eviction is least-recently-used. The stored *OCRResult
// is shared and must be treated as read-only.
type ocrCache struct {
	mu         sync.Mutex
	ll         *list.List
	m          map[ocrKey]*list.Element
	maxEntries int
	maxBytes   int64
	bytes      int64
}

func newOCRCache(maxEntries int, maxBytes int64) *ocrCache {
	return &ocrCache{
		ll:         list.New(),
		m:          make(map[ocrKey]*list.Element),
		maxEntries: maxEntries,
		maxBytes:   maxBytes,
	}
}

func (c *ocrCache) get(key ocrKey) (*OCRResult, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.m[key]
	if !ok {
		return nil, false
	}
	c.ll.MoveToFront(el)
	return el.Value.(*ocrCacheEntry).res, true
}

func (c *ocrCache) put(key ocrKey, res *OCRResult) {
	size := ocrResultSize(res)
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.m[key]; ok {
		e := el.Value.(*ocrCacheEntry)
		c.bytes += size - e.size
		e.res, e.size = res, size
		c.ll.MoveToFront(el)
	} else {
		el := c.ll.PushFront(&ocrCacheEntry{key: key, res: res, size: size})
		c.m[key] = el
		c.bytes += size
	}
	for c.ll.Len() > c.maxEntries || c.bytes > c.maxBytes {
		el := c.ll.Back()
		if el == nil {
			break
		}
		e := el.Value.(*ocrCacheEntry)
		c.bytes -= e.size
		delete(c.m, e.key)
		c.ll.Remove(el)
	}
}

// ocrResultSize approximates the retained bytes of a result: the text plus a
// per-line estimate for the line string, confidence, and bounding box. Good
// enough for the byte ceiling — exact accounting isn't worth the cost.
func ocrResultSize(res *OCRResult) int64 {
	n := int64(len(res.Text))
	for _, l := range res.Lines {
		n += int64(len(l.Text)) + 32
	}
	return n
}
