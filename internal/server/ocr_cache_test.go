package server

import (
	"context"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/textract"
	"github.com/aws/aws-sdk-go-v2/service/textract/types"
)

func ocrRes(text string) *OCRResult {
	return &OCRResult{Text: text, Lines: []OCRLine{{Text: text, Confidence: 99}}}
}

func TestOCRCacheGetPut(t *testing.T) {
	c := newOCRCache(4, 1<<20)
	res := ocrRes("hello")
	c.put([32]byte{1}, res)
	if got, ok := c.get([32]byte{1}); !ok || got != res {
		t.Fatalf("expected hit, got ok=%v", ok)
	}
	if _, ok := c.get([32]byte{2}); ok {
		t.Fatalf("expected miss for unknown key")
	}
}

func TestOCRCacheLRUEviction(t *testing.T) {
	c := newOCRCache(2, 1<<20)
	c.put([32]byte{1}, ocrRes("a"))
	c.put([32]byte{2}, ocrRes("b"))
	c.put([32]byte{3}, ocrRes("c")) // evicts key 1 (LRU)
	if _, ok := c.get([32]byte{1}); ok {
		t.Fatalf("key 1 should have been evicted")
	}
	if _, ok := c.get([32]byte{3}); !ok {
		t.Fatalf("key 3 should be present")
	}
}

func TestOCRCacheGetRefreshesRecency(t *testing.T) {
	c := newOCRCache(2, 1<<20)
	c.put([32]byte{1}, ocrRes("a"))
	c.put([32]byte{2}, ocrRes("b"))
	c.get([32]byte{1})               // refresh key 1
	c.put([32]byte{3}, ocrRes("c")) // evicts key 2 (now LRU)
	if _, ok := c.get([32]byte{1}); !ok {
		t.Fatalf("key 1 should survive after refresh")
	}
	if _, ok := c.get([32]byte{2}); ok {
		t.Fatalf("key 2 should have been evicted")
	}
}

func TestOCRCacheByteCap(t *testing.T) {
	c := newOCRCache(100, 250)
	big := ocrRes(strings.Repeat("x", 100)) // ocrResultSize = 232
	c.put([32]byte{1}, big)
	c.put([32]byte{2}, big) // 464 > 250 -> evicts key 1
	if _, ok := c.get([32]byte{1}); ok {
		t.Fatalf("key 1 should be evicted by byte cap")
	}
	if _, ok := c.get([32]byte{2}); !ok {
		t.Fatalf("key 2 should be present")
	}
}

func TestOCRCacheReplaceUpdatesSize(t *testing.T) {
	c := newOCRCache(2, 1<<20)
	big := ocrRes(strings.Repeat("b", 100))
	c.put([32]byte{1}, ocrRes("a"))
	c.put([32]byte{1}, big)
	if c.ll.Len() != 1 {
		t.Fatalf("expected 1 entry after replace, got %d", c.ll.Len())
	}
	if c.bytes != ocrResultSize(big) {
		t.Fatalf("bytes not updated: got %d want %d", c.bytes, ocrResultSize(big))
	}
	if got, ok := c.get([32]byte{1}); !ok || got != big {
		t.Fatalf("replaced value not returned")
	}
}

// countingTextract is a fake textractAPI that counts DetectDocumentText calls.
type countingTextract struct{ calls int }

func (f *countingTextract) DetectDocumentText(_ context.Context, _ *textract.DetectDocumentTextInput, _ ...func(*textract.Options)) (*textract.DetectDocumentTextOutput, error) {
	f.calls++
	return &textract.DetectDocumentTextOutput{
		Blocks: []types.Block{
			{BlockType: types.BlockTypeLine, Text: aws.String("TEST LINE"), Confidence: aws.Float32(99)},
		},
	}, nil
}

func TestExtractMemoizes(t *testing.T) {
	fake := &countingTextract{}
	ocr := &OCRClient{c: fake, cache: newOCRCache(4, 1<<20)}
	img := []byte("image-bytes")
	for i := 0; i < 3; i++ {
		if _, err := ocr.Extract(context.Background(), img); err != nil {
			t.Fatal(err)
		}
	}
	if fake.calls != 1 {
		t.Fatalf("expected 1 Textract call across 3 identical extracts, got %d", fake.calls)
	}
}
