package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"sync/atomic"
	"time"

	"Bevcheck/internal/model"
)

// jobStatus is the coarse lifecycle state of an async submission.
type jobStatus string

const (
	jobRunning jobStatus = "running"
	jobDone    jobStatus = "done"
)

// pair is one (image, application) verification unit. Either img (raw bytes
// from a multipart upload) or url (a JSONL batch image_url to download) is set.
type pair struct {
	img []byte
	url string
	app *model.Application
}

// Item is one verification. Written by a single goroutine; readers use result().
type Item struct {
	mu       sync.RWMutex
	ID       string
	Status   jobStatus
	Overall  string
	Error    string
	Criteria []CriterionResult
	OCRText  string
	OCRConf  float32
	Lines    []OCRLine
}

// ItemResult is the JSON shape of an Item.
type ItemResult struct {
	ID       string            `json:"id"`
	Status   jobStatus         `json:"status"`
	Overall  string            `json:"overall,omitempty"`
	Error    string            `json:"error,omitempty"`
	Criteria []CriterionResult `json:"criteria"`
	OCRText  string            `json:"ocr_text,omitempty"`
	OCRConf  float32           `json:"ocr_confidence"`
	Lines    []OCRLine         `json:"lines,omitempty"`
}

func (it *Item) finish(res *OCRResult, text string, minConf float32, overall string, crits []CriterionResult) {
	it.mu.Lock()
	defer it.mu.Unlock()
	it.Status = jobDone
	it.Overall = overall
	it.Criteria = crits
	it.OCRText = text
	it.OCRConf = minConf
	it.Lines = res.Lines
}

func (it *Item) fail(err error) {
	it.mu.Lock()
	defer it.mu.Unlock()
	it.Status = jobDone
	it.Error = err.Error()
}

// needsCorrection marks the item as needing attention (not a hard failure) with
// a single criterion — used for recoverable input problems like an image URL
// that could not be downloaded.
func (it *Item) needsCorrection(reason string) {
	it.mu.Lock()
	defer it.mu.Unlock()
	it.Status = jobDone
	it.Overall = "Needs Correction"
	it.Criteria = []CriterionResult{{Name: "image_download", Status: statusNeedsCorrection, Reason: reason}}
}

func (it *Item) result() ItemResult {
	it.mu.RLock()
	defer it.mu.RUnlock()
	return ItemResult{
		ID: it.ID, Status: it.Status, Overall: it.Overall, Error: it.Error,
		Criteria: it.Criteria, OCRText: it.OCRText, OCRConf: it.OCRConf, Lines: it.Lines,
	}
}

// Job is one submission of 1..N items — the single path and the batch path are
// the same thing; a single upload is just a batch of one.
type Job struct {
	mu        sync.RWMutex
	ID        string
	Status    jobStatus
	Items     []*Item
	CreatedAt time.Time
	remaining int32 // unresolved items; decremented as each resolves
}

// JobResult is the JSON shape of a Job.
type JobResult struct {
	ID     string       `json:"id"`
	Status jobStatus    `json:"status"`
	Items  []ItemResult `json:"items"`
}

func (j *Job) result() JobResult {
	j.mu.RLock()
	status := j.Status
	j.mu.RUnlock()
	items := make([]ItemResult, 0, len(j.Items))
	for _, it := range j.Items {
		items = append(items, it.result())
	}
	return JobResult{ID: j.ID, Status: status, Items: items}
}

type jobStore struct {
	mu   sync.RWMutex
	jobs map[string]*Job
	ctx  context.Context // server lifetime; canceled on shutdown
}

func newJobStore(ctx context.Context) *jobStore {
	s := &jobStore{jobs: map[string]*Job{}, ctx: ctx}
	go s.sweep()
	return s
}

func (s *jobStore) sweep() {
	for range time.Tick(5 * time.Minute) {
		cutoff := time.Now().Add(-30 * time.Minute)
		s.mu.Lock()
		for id, j := range s.jobs {
			if j.CreatedAt.Before(cutoff) {
				delete(s.jobs, id)
			}
		}
		s.mu.Unlock()
	}
}

// submit creates a job with one item per pair and runs each item's OCR +
// criteria in its own goroutine.
func (s *jobStore) submit(ocr *OCRClient, pairs []pair) string {
	job := &Job{ID: newID(), Status: jobRunning, CreatedAt: time.Now(), remaining: int32(len(pairs))}
	for range pairs {
		job.Items = append(job.Items, &Item{ID: newID(), Status: jobRunning})
	}
	s.mu.Lock()
	s.jobs[job.ID] = job
	s.mu.Unlock()

	// Cap concurrent items so a large batch cannot fan out unbounded goroutines
	// (each holding an image and a Textract call) at once.
	sem := make(chan struct{}, maxConcurrentItems)
	for i, p := range pairs {
		it := job.Items[i]
		go func(it *Item, p pair) {
			sem <- struct{}{}
			defer func() { <-sem }()

			img := p.img
			if p.url != "" {
				var err error
				img, err = downloadImage(p.url)
				if err != nil {
					it.needsCorrection("image download: " + err.Error())
					s.itemDone(job)
					return
				}
			}
			ctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
			defer cancel()
			res, err := ocr.Extract(ctx, img)
			if err != nil {
				it.fail(err)
				s.itemDone(job)
				return
			}
			text := collapseWS(res.Text)
			minConf := minLineConfidence(res.Lines)
			overall, crits := evaluate(&verifyContext{app: p.app, ocr: text, minConf: minConf})
			it.finish(res, text, minConf, overall, crits)
			s.itemDone(job)
		}(it, p)
	}
	return job.ID
}

// itemDone marks one item resolved and flips the job to done once the last
// item has resolved. O(1) per item instead of rescanning every item.
func (s *jobStore) itemDone(job *Job) {
	if atomic.AddInt32(&job.remaining, -1) == 0 {
		job.mu.Lock()
		job.Status = jobDone
		job.mu.Unlock()
	}
}

func (s *jobStore) getJob(id string) (*Job, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	j, ok := s.jobs[id]
	return j, ok
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
