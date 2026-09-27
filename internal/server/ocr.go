package server

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/textract"
	"github.com/aws/aws-sdk-go-v2/service/textract/types"
)

// textractAPI is the slice of the Textract client the OCR path needs, kept as
// an interface so tests can substitute a fake that counts calls.
type textractAPI interface {
	DetectDocumentText(ctx context.Context, params *textract.DetectDocumentTextInput, optFns ...func(*textract.Options)) (*textract.DetectDocumentTextOutput, error)
}

// OCRClient wraps Textract DetectDocumentText (raw/pure text extraction).
// Credentials come from the EC2 instance role (IMDS) via the SDK default chain.
// Results are memoized by image hash in an in-memory LRU cache so re-submitting
// the same image bytes skips a Textract call.
type OCRClient struct {
	c     textractAPI
	cache *ocrCache
}

func NewOCRClient(ctx context.Context, region string) (*OCRClient, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return nil, err
	}
	return &OCRClient{
		c:     textract.NewFromConfig(cfg),
		cache: newOCRCache(ocrCacheMaxEntries, ocrCacheMaxBytes),
	}, nil
}

type BBox struct {
	Left   float32 `json:"left"`
	Top    float32 `json:"top"`
	Width  float32 `json:"width"`
	Height float32 `json:"height"`
}

type OCRLine struct {
	Text       string  `json:"text"`
	Confidence float32 `json:"confidence"`
	BBox       BBox    `json:"bbox"`
}

type OCRResult struct {
	Text  string    `json:"text"`
	Lines []OCRLine `json:"lines"`
}

// Extract runs DetectDocumentText and returns line-level text in reading order
// (top-to-bottom, left-to-right), memoized by the SHA-256 of the image bytes.
// Callers must treat the returned *OCRResult as read-only: a cache hit shares
// the stored value.
func (o *OCRClient) Extract(ctx context.Context, img []byte) (*OCRResult, error) {
	if o.cache == nil {
		return o.extract(ctx, img)
	}
	key := sha256.Sum256(img)
	if res, ok := o.cache.get(key); ok {
		return res, nil
	}
	res, err := o.extract(ctx, img)
	if err != nil {
		return nil, err
	}
	o.cache.put(key, res)
	return res, nil
}

// extract performs the uncached DetectDocumentText call.
func (o *OCRClient) extract(ctx context.Context, img []byte) (*OCRResult, error) {
	out, err := o.c.DetectDocumentText(ctx, &textract.DetectDocumentTextInput{
		Document: &types.Document{Bytes: img},
	})
	if err != nil {
		return nil, fmt.Errorf("textract DetectDocumentText: %w", err)
	}

	lines := make([]OCRLine, 0, len(out.Blocks))
	for _, b := range out.Blocks {
		if b.BlockType != types.BlockTypeLine || b.Text == nil {
			continue
		}
		l := OCRLine{Text: *b.Text, Confidence: aws.ToFloat32(b.Confidence)}
		if b.Geometry != nil && b.Geometry.BoundingBox != nil {
			bb := b.Geometry.BoundingBox
			l.BBox = BBox{Left: bb.Left, Top: bb.Top, Width: bb.Width, Height: bb.Height}
		}
		lines = append(lines, l)
	}
	sort.SliceStable(lines, func(i, j int) bool {
		if lines[i].BBox.Top != lines[j].BBox.Top {
			return lines[i].BBox.Top < lines[j].BBox.Top
		}
		return lines[i].BBox.Left < lines[j].BBox.Left
	})

	texts := make([]string, len(lines))
	for i, l := range lines {
		texts[i] = l.Text
	}
	return &OCRResult{Text: strings.Join(texts, "\n"), Lines: lines}, nil
}
