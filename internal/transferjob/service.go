// Package transferjob runs the single asynchronous fulfillment import operation.
// It reuses the importer; it is not a general-purpose queue or scheduler.
package transferjob

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/YoooClaw/cli/internal/fsutil"
)

type Request struct {
	TransferID string `json:"transferId"`
	FileTaskID string `json:"fileTaskId"`
}
type Event struct {
	TransferID   string `json:"transferId"`
	EventType    string `json:"eventType"`
	ErrorMessage string `json:"errorMessage,omitempty"`
}

var safeErrorRE = regexp.MustCompile(`^TRANSFER_[A-Z0-9_]{1,100}$`)

var idRE = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
var fileRE = regexp.MustCompile(`^[a-f0-9]{32}$`)

func (r Request) Valid() bool {
	return idRE.MatchString(r.TransferID) && fileRE.MatchString(r.FileTaskID)
}

var (
	ErrInvalid  = errors.New("TRANSFER_INVALID_REQUEST")
	ErrConflict = errors.New("TRANSFER_CONFLICT")
	ErrBusy     = errors.New("TRANSFER_BUSY")
	ErrNotReady = errors.New("TRANSFER_NOT_READY")
)

type Dependencies struct {
	Identity func(label string) string
	Run      func(context.Context, Request, string, func(string) error) error
	Report   func(context.Context, string, Event) error
	Warn     func(string)
	Info     func(string)
	Timeout  time.Duration
}
type record struct {
	Request         Request   `json:"request"`
	Label           string    `json:"label"`
	Identity        string    `json:"identity"`
	Deadline        time.Time `json:"deadline"`
	State           string    `json:"state"`
	Result          *Event    `json:"result,omitempty"`
	Reported        bool      `json:"reported"`
	reportAttempted bool      // bounded retries once per process; restart retries only the saved result
}
type Service struct {
	mu     sync.Mutex
	dir    string
	deps   Dependencies
	jobs   map[string]*record
	ctx    context.Context
	cancel context.CancelFunc
	wake   chan struct{}
	done   chan struct{}
}

func New(dir string, deps Dependencies) (*Service, error) {
	if deps.Identity == nil || deps.Run == nil || deps.Report == nil || deps.Timeout <= 0 {
		return nil, ErrNotReady
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	s := &Service{dir: dir, deps: deps, jobs: map[string]*record{}, wake: make(chan struct{}, 1), done: make(chan struct{})}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		raw, e := os.ReadFile(filepath.Join(dir, entry.Name()))
		if e != nil {
			return nil, e
		}
		var job record
		if json.Unmarshal(raw, &job) != nil || !job.Request.Valid() || entry.Name() != job.Request.TransferID+".json" {
			return nil, errors.New("TRANSFER_JOB_CORRUPT")
		}
		// An interrupted import can have committed files. Never replay it silently.
		if job.State == "running" {
			job.State = "done"
			s.info(job.Request.TransferID, "import.interrupted")
			job.Result = &Event{job.Request.TransferID, "IMPORT_FAILED", "迁入进程中断，请联系客服核查"}
			if e = s.save(&job); e != nil {
				return nil, e
			}
		}
		s.info(job.Request.TransferID, fmt.Sprintf("import.recovered state=%s reported=%t", job.State, job.Reported))
		s.jobs[job.Request.TransferID] = &job
	}
	if s.deps.Info != nil {
		s.deps.Info(fmt.Sprintf("[transfer] import.jobs_loaded count=%d", len(s.jobs)))
	}
	go s.loop()
	s.signal()
	return s, nil
}
func (s *Service) save(job *record) error {
	raw, err := json.Marshal(job)
	if err != nil {
		return err
	}
	return fsutil.WriteAtomic(filepath.Join(s.dir, job.Request.TransferID+".json"), raw, 0600)
}
func (s *Service) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}
func (s *Service) Accept(req Request, label string) error {
	if !req.Valid() {
		return ErrInvalid
	}
	identity := s.deps.Identity(label)
	if identity == "" {
		return ErrNotReady
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx.Err() != nil {
		return ErrNotReady
	}
	if old := s.jobs[req.TransferID]; old != nil {
		if old.Request != req || old.Identity != identity || old.Label != label {
			return ErrConflict
		}
		s.info(req.TransferID, "import.duplicate state="+old.State)
		return nil
	}
	for _, job := range s.jobs {
		if job.State == "queued" || job.State == "running" {
			return ErrBusy
		}
	}
	job := &record{Request: req, Label: label, Identity: identity, Deadline: time.Now().Add(s.deps.Timeout), State: "queued"}
	if err := s.save(job); err != nil {
		return err
	}
	s.jobs[req.TransferID] = job
	s.info(req.TransferID, "import.accepted deadline="+job.Deadline.UTC().Format(time.RFC3339))
	s.signal()
	return nil
}
func (s *Service) Cancel() { s.cancel() }
func (s *Service) Stop()   { s.cancel(); <-s.done }
func (s *Service) warn(msg string) {
	if s.deps.Warn != nil {
		s.deps.Warn(msg)
	}
}
func (s *Service) info(id, event string) {
	if s.deps.Info != nil {
		s.deps.Info(fmt.Sprintf("[transfer] transferId=%s %s", id, event))
	}
}
func (s *Service) valid(j *record) bool {
	return j.Identity != "" && s.deps.Identity(j.Label) == j.Identity
}
func (s *Service) emit(ctx context.Context, j *record, event Event) error {
	// Four attempts = initial request plus three retries. No tight permanent-error loop.
	var err error
	for i := 0; i < 4; i++ {
		if !s.valid(j) {
			return errors.New("TRANSFER_IDENTITY_CHANGED")
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		s.info(j.Request.TransferID, fmt.Sprintf("event.sending event=%s attempt=%d", event.EventType, i+1))
		err = s.deps.Report(ctx, j.Label, event)
		if err == nil {
			s.info(j.Request.TransferID, "event.reported event="+event.EventType)
			return nil
		}
		s.warn(fmt.Sprintf("[transfer] transferId=%s event.report_failed event=%s attempt=%d", j.Request.TransferID, event.EventType, i+1))
		var permanent interface{ Permanent() bool }
		if errors.As(err, &permanent) && permanent.Permanent() {
			s.warn("[transfer] transferId=" + j.Request.TransferID + " event.report_blocked")
			return err
		}
		if i < 3 {
			timer := time.NewTimer(time.Duration(i+1) * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	return err
}
func (s *Service) loop() {
	defer close(s.done)
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.wake:
			s.process()
		}
	}
}
func (s *Service) process() {
	s.mu.Lock()
	jobs := make([]*record, 0, len(s.jobs))
	for _, j := range s.jobs {
		jobs = append(jobs, j)
	}
	s.mu.Unlock()
	for _, j := range jobs {
		if s.ctx.Err() != nil {
			return
		}
		s.mu.Lock()
		queued := j.State == "queued"
		if queued {
			j.State = "running"
			if err := s.save(j); err != nil {
				j.State = "queued"
				s.mu.Unlock()
				s.warn("TRANSFER_JOB_SAVE_FAILED")
				continue
			}
		}
		s.mu.Unlock()
		if queued {
			started := time.Now()
			s.info(j.Request.TransferID, "import.started")
			ctx, cancel := context.WithDeadline(s.ctx, j.Deadline)
			err := s.emit(ctx, j, Event{TransferID: j.Request.TransferID, EventType: "IMPORT_STARTED"})
			if err == nil {
				err = s.deps.Run(ctx, j.Request, j.Label, func(kind string) error {
					return s.emit(ctx, j, Event{TransferID: j.Request.TransferID, EventType: kind})
				})
			}
			if err == nil {
				err = ctx.Err()
			}
			cancel()
			result := &Event{TransferID: j.Request.TransferID, EventType: "IMPORT_SUCCEEDED"}
			if err != nil {
				result.EventType = "IMPORT_FAILED"
				result.ErrorMessage = "迁入未完成，请联系客服核查"
				if errors.Is(err, context.DeadlineExceeded) {
					result.ErrorMessage = "迁入超过配置时限，请联系客服"
				}
				s.warn("[transfer] transferId=" + j.Request.TransferID + " import.failed code=" + safeError(err))
			}
			s.info(j.Request.TransferID, fmt.Sprintf("import.finished event=%s elapsedMs=%d", result.EventType, time.Since(started).Milliseconds()))
			s.mu.Lock()
			j.State = "done"
			j.Result = result
			if e := s.save(j); e != nil {
				s.mu.Unlock()
				s.warn("TRANSFER_RESULT_SAVE_FAILED")
				continue
			}
			s.mu.Unlock()
		}
		if j.Result != nil && !j.Reported && !j.reportAttempted && s.valid(j) {
			s.mu.Lock()
			if err := s.save(j); err != nil {
				s.mu.Unlock()
				s.warn("TRANSFER_RESULT_SAVE_FAILED")
				continue
			}
			j.reportAttempted = true
			s.mu.Unlock()
			ctx, cancel := context.WithTimeout(s.ctx, 2*time.Minute)
			err := s.emit(ctx, j, *j.Result)
			cancel()
			if err != nil {
				s.warn("[transfer] transferId=" + j.Request.TransferID + " TRANSFER_RESULT_REPORT_FAILED")
				continue
			}
			s.mu.Lock()
			j.Reported = true
			if err = s.save(j); err != nil {
				j.Reported = false
				s.warn("TRANSFER_RESULT_SAVE_FAILED")
			}
			s.mu.Unlock()
		}
	}
}

// Never log raw provider errors: they can contain signed URLs or user content.
func safeError(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "TRANSFER_IMPORT_TIMEOUT"
	}
	if errors.Is(err, context.Canceled) {
		return "TRANSFER_IMPORT_CANCELLED"
	}
	if err != nil && safeErrorRE.MatchString(err.Error()) {
		return err.Error()
	}
	return "TRANSFER_IMPORT_FAILED"
}
