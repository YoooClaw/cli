// Package assets implements bounded, read-only views over existing asset stores.
package assets

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"os"
	"sync"
	"time"
)

const (
	ResponseLimit   = 256 << 10
	ItemLimit       = 64 << 10
	HealthItemLimit = 240 << 10
	LineLimit       = 1 << 20
	ScanLimit       = 4 << 20
	BlockSize       = 64 << 10
	IndexLimit      = 32 << 20
	CursorLimit     = 4096
	// Reserve the maximum cursor plus all warning codes and envelope punctuation.
	envelopeReserve = 8192
)

var limits = map[string][2]int{
	"notifications": {50, 100}, "locations": {50, 100}, "calendars": {50, 100},
	"health-data": {7, 14}, "web-pages": {20, 50},
}

func Supported(kind string) bool { _, ok := limits[kind]; return ok }

type Source struct {
	Dir          string
	WithReadView func(func() error) error
}

func (s Source) view(fn func() error) error {
	if s.WithReadView != nil {
		return s.WithReadView(fn)
	}
	return fn()
}

type Item struct {
	RecordID        string         `json:"recordId"`
	StorageDate     *string        `json:"storageDate"`
	Status          string         `json:"status"`
	Data            map[string]any `json:"data"`
	TruncatedFields []string       `json:"truncatedFields"`
	SourceBytes     int64          `json:"sourceBytes,omitempty"`
}

type Warning struct {
	Code  string `json:"code"`
	Count int    `json:"count"`
}

type Response struct {
	OK         bool      `json:"ok"`
	Items      []Item    `json:"items"`
	NextCursor *string   `json:"nextCursor"`
	Warnings   []Warning `json:"warnings"`
}

type Error struct {
	Status    int    `json:"-"`
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
	Action    string `json:"action"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }
func failure(status int, code, message, action string) *Error {
	return &Error{Status: status, Code: code, Message: message, Retryable: action == "retry", Action: action}
}
func invalid() *Error { return failure(400, "INVALID_PARAMS", "Invalid query parameters", "none") }
func expired() *Error {
	return failure(410, "CURSOR_EXPIRED", "The source or cursor has expired; refresh this category", "refresh")
}
func readFailed() *Error {
	return failure(500, "SOURCE_READ_FAILED", "Unable to read asset source", "retry")
}
func tooLarge() *Error {
	return failure(503, "SOURCE_TOO_LARGE", "Source exceeds the supported read budget", "none")
}
func changed() *Error {
	return failure(409, "SOURCE_CHANGED", "Source changed during reading", "retry")
}

type fileIdentity struct {
	id      string
	info    os.FileInfo
	touched uint64
}

type Service struct {
	profile  string
	sources  map[string]Source
	key      [32]byte
	boot     string
	now      func() time.Time
	mu       sync.Mutex
	files    map[string]fileIdentity
	sequence uint64
}

func New(profile string, sources map[string]Source) *Service {
	s := &Service{profile: profile, sources: sources, now: time.Now, files: map[string]fileIdentity{}}
	_, _ = rand.Read(s.key[:])
	s.boot = digest(string(s.key[:]))[:32]
	return s
}

func digest(parts ...string) string {
	b, _ := json.Marshal(parts)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

type page struct {
	Response
	bytes int
}

func newPage() *page {
	return &page{Response: Response{OK: true, Items: []Item{}, Warnings: []Warning{}}, bytes: envelopeReserve}
}
func (p *page) warn(code string) {
	for i := range p.Warnings {
		if p.Warnings[i].Code == code {
			p.Warnings[i].Count++
			return
		}
	}
	p.Warnings = append(p.Warnings, Warning{Code: code, Count: 1})
}
func (p *page) add(it Item) bool {
	raw, _ := json.Marshal(it)
	if p.bytes+len(raw)+1 > ResponseLimit {
		return false
	}
	p.bytes += len(raw) + 1
	p.Items = append(p.Items, it)
	return true
}

// Read serves all assets in the authenticated caller's single-owner profile.
// Source labels remain display metadata and never narrow the result set.
func (s *Service) Read(ctx context.Context, kind string, values url.Values) (Response, *Error) {
	req, err := s.request(kind, values)
	if err != nil {
		return Response{}, err
	}
	p := newPage()
	switch kind {
	case "health-data":
		err = s.readHealth(ctx, req, p)
	case "web-pages":
		err = s.readWeb(ctx, req, p)
	default:
		err = s.readLines(ctx, req, p)
	}
	if err != nil {
		return Response{}, err
	}
	raw, marshalErr := json.Marshal(p.Response)
	if marshalErr != nil || len(raw) > ResponseLimit {
		return Response{}, readFailed()
	}
	return p.Response, nil
}

func (s *Service) next(p *page, c cursor) {
	c.Exp = s.now().Add(time.Hour).Unix()
	token := s.sign(c)
	p.NextCursor = &token
}
