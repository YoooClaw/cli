// Package data stores immutable opaque bytes; it never executes or parses content.
package data

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/YoooClaw/cli/internal/clientlabel"
	"github.com/YoooClaw/cli/internal/config"
	"github.com/YoooClaw/cli/internal/errs"
	"github.com/YoooClaw/cli/internal/fsutil"
)

const HardMaxBytes int64 = 64 << 20

func Error(code, message string) error { return errs.New("YOOOCLAW_DATA_"+code, message) }
func Limits(c config.DataSection) config.DataSection {
	if c.MaxObjectBytes <= 0 {
		c.MaxObjectBytes = 32 << 20
	}
	if c.MaxObjectBytes > HardMaxBytes {
		c.MaxObjectBytes = HardMaxBytes
	}
	if c.MaxInlineBytes <= 0 {
		c.MaxInlineBytes = 512 << 10
	}
	if c.MaxInlineBytes > c.MaxObjectBytes {
		c.MaxInlineBytes = c.MaxObjectBytes
	}
	if c.QuotaBytes <= 0 {
		c.QuotaBytes = 1 << 30
	}
	return c
}

type Metadata struct {
	Name           string          `json:"name"`
	Title          string          `json:"title"`
	MIME           string          `json:"mime,omitempty"`
	Tags           []string        `json:"tags"`
	Note           string          `json:"note"`
	Source         string          `json:"source"`
	CapturedAt     *time.Time      `json:"capturedAt"`
	Meta           json.RawMessage `json:"meta"`
	IdempotencyKey string          `json:"idempotencyKey,omitempty"`
}
type Object struct {
	SchemaVersion int             `json:"schemaVersion"`
	ID            string          `json:"id"`
	SHA256        string          `json:"sha256"`
	SizeBytes     int64           `json:"sizeBytes"`
	MediaType     string          `json:"mediaType"`
	DeclaredType  string          `json:"declaredType"`
	Format        string          `json:"format"`
	Name          string          `json:"name"`
	Title         string          `json:"title"`
	Tags          []string        `json:"tags"`
	Note          string          `json:"note"`
	Source        string          `json:"source"`
	ClientLabel   string          `json:"clientLabel"`
	CreatedAt     time.Time       `json:"createdAt"`
	CapturedAt    *time.Time      `json:"capturedAt"`
	Preview       *string         `json:"preview"`
	Meta          json.RawMessage `json:"meta"`
	BlobPath      string          `json:"blobPath"`
}
type index struct {
	Version int               `json:"version"`
	Objects []Object          `json:"objects"`
	Keys    map[string]string `json:"idempotencyKeys"`
}
type Store struct {
	Dir    string
	Config config.DataSection
	mu     *sync.Mutex
}

var locks sync.Map

func New(dir string, c config.DataSection) *Store {
	abs, _ := filepath.Abs(dir)
	lock, _ := locks.LoadOrStore(abs, &sync.Mutex{})
	return &Store{abs, Limits(c), lock.(*sync.Mutex)}
}

var uuidRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
var hashRE = regexp.MustCompile(`^[0-9a-f]{64}$`)

func blobPath(hash string) string { return "blobs/" + hash[:2] + "/" + hash }
func (s *Store) read() (index, error) {
	x := index{Version: 1, Objects: []Object{}, Keys: map[string]string{}}
	b, e := os.ReadFile(filepath.Join(s.Dir, "index.json"))
	if os.IsNotExist(e) {
		return x, nil
	}
	if e != nil {
		return x, e
	}
	if e = json.Unmarshal(b, &x); e != nil {
		return x, e
	}
	if x.Version != 1 {
		return x, fmt.Errorf("unsupported data index version")
	}
	if x.Keys == nil {
		x.Keys = map[string]string{}
	}
	for _, o := range x.Objects {
		if !uuidRE.MatchString(o.ID) || !hashRE.MatchString(o.SHA256) || o.BlobPath != blobPath(o.SHA256) || o.SizeBytes < 0 {
			return x, fmt.Errorf("invalid data index object")
		}
	}
	return x, nil
}
func (s *Store) Check() error { _, e := s.read(); return e }
func (s *Store) save(x index) error {
	b, e := json.Marshal(x)
	if e != nil {
		return e
	}
	return writeIndex(filepath.Join(s.Dir, "index.json"), b)
}
func (s *Store) Init() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range []string{s.Dir, filepath.Join(s.Dir, "blobs"), filepath.Join(s.Dir, "tmp")} {
		if e := fsutil.EnsureDir(p, 0700); e != nil {
			return e
		}
		if e := rejectSymlink(s.Dir, p); e != nil {
			return e
		}
	}
	x, e := s.read()
	if e != nil {
		return e
	}
	if e = s.retention(&x); e != nil {
		return e
	}
	_, e = s.gc(x, false)
	return e
}
func validate(m *Metadata) error {
	m.Name = filepath.Base(strings.ReplaceAll(m.Name, "\\", "/"))
	if m.Name == "." {
		m.Name = ""
	}
	m.Name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, m.Name)
	m.Name = strings.ReplaceAll(m.Name, "..", "")
	if len(m.Name) > 255 || len(m.Title) > 512 || len(m.Note) > 4096 || len(m.Source) > 128 || len(m.MIME) > 255 || len(m.IdempotencyKey) > 256 {
		return Error("INVALID_PARAMS", "metadata field exceeds limit")
	}
	tags := []string{}
	seen := map[string]bool{}
	for _, t := range m.Tags {
		t = strings.TrimSpace(t)
		if utf8.RuneCountInString(t) > 64 {
			return Error("INVALID_PARAMS", "tag exceeds 64 characters")
		}
		if t != "" && !seen[t] {
			tags = append(tags, t)
			seen[t] = true
		}
	}
	if len(tags) > 32 {
		return Error("INVALID_PARAMS", "too many tags")
	}
	m.Tags = tags
	if len(m.Meta) == 0 {
		m.Meta = json.RawMessage(`{}`)
	}
	if len(m.Meta) > 16<<10 {
		return Error("INVALID_META", "meta exceeds 16 KiB")
	}
	var v map[string]any
	if json.Unmarshal(m.Meta, &v) != nil || v == nil {
		return Error("INVALID_META", "meta must be a JSON object")
	}
	if depth(v) > 16 {
		return Error("INVALID_META", "meta nesting exceeds 16")
	}
	return nil
}
func depth(v any) int {
	d := 0
	switch x := v.(type) {
	case map[string]any:
		for _, y := range x {
			if n := depth(y); n > d {
				d = n
			}
		}
		return d + 1
	case []any:
		for _, y := range x {
			if n := depth(y); n > d {
				d = n
			}
		}
		return d + 1
	}
	return 0
}

type PutResult struct {
	OK      bool   `json:"ok"`
	Data    Object `json:"data"`
	Deduped bool   `json:"deduped"`
}

func (s *Store) Put(r io.Reader, m Metadata, label string) (result PutResult, err error) {
	if e := validate(&m); e != nil {
		return result, e
	}
	if label == "" {
		label = "local"
	}
	// One profile writer lock also protects in-flight temporary files from GC.
	s.mu.Lock()
	defer s.mu.Unlock()
	defer func() {
		if err != nil {
			var de *errs.Error
			if !errors.As(err, &de) {
				err = Error("WRITE_FAILED", "data write failed")
			}
		}
	}()
	for _, p := range []string{s.Dir, filepath.Join(s.Dir, "tmp"), filepath.Join(s.Dir, "blobs")} {
		if err = fsutil.EnsureDir(p, 0700); err != nil {
			return
		}
		if err = rejectSymlink(s.Dir, p); err != nil {
			return
		}
	}
	x, e := s.read()
	if e != nil {
		return result, e
	}
	if e = s.retention(&x); e != nil {
		return result, e
	}
	f, e := os.CreateTemp(filepath.Join(s.Dir, "tmp"), "put-")
	if e != nil {
		return result, e
	}
	defer os.Remove(f.Name())
	defer f.Close()
	h := sha256.New()
	validator := &utf8Check{}
	n, e := io.Copy(io.MultiWriter(f, h, validator), io.LimitReader(r, s.Config.MaxObjectBytes+1))
	if n > s.Config.MaxObjectBytes {
		return result, Error("TOO_LARGE", "object exceeds maxObjectBytes")
	}
	if e != nil {
		var max *http.MaxBytesError
		if errors.As(e, &max) {
			return result, Error("TOO_LARGE", "request exceeds limit")
		}
		return result, e
	}
	sum := hex.EncodeToString(h.Sum(nil))
	key := label + "\x00" + m.IdempotencyKey
	if m.IdempotencyKey != "" {
		if id := x.Keys[key]; id != "" {
			for _, o := range x.Objects {
				if o.ID == id {
					if o.SHA256 != sum {
						return result, Error("IDEMPOTENCY_CONFLICT", "key already used for different content")
					}
					return PutResult{true, o, true}, nil
				}
			}
		}
	}
	rel := blobPath(sum)
	dest := filepath.Join(s.Dir, filepath.FromSlash(rel))
	dedup := false
	if info, e := os.Lstat(dest); e == nil {
		if !info.Mode().IsRegular() {
			return result, fmt.Errorf("invalid blob")
		}
		p, ok := fsutil.ResolveExistingRegularFile(s.Dir, rel)
		if !ok {
			return result, fmt.Errorf("invalid blob path")
		}
		existing, e := os.Open(p)
		if e != nil {
			return result, e
		}
		hh := sha256.New()
		size, e := io.Copy(hh, existing)
		existing.Close()
		if e != nil || size != n || hex.EncodeToString(hh.Sum(nil)) != sum {
			return result, fmt.Errorf("blob integrity failure")
		}
		dedup = true
	} else if !os.IsNotExist(e) {
		return result, e
	}
	bytes, e := s.diskBytes()
	if e != nil {
		return result, e
	}
	if !dedup && n > s.Config.QuotaBytes-bytes {
		return result, Error("QUOTA_EXCEEDED", "profile quota exceeded")
	}
	if e = f.Sync(); e != nil {
		return result, e
	}
	if e = f.Close(); e != nil {
		return result, e
	}
	if !dedup {
		if e = fsutil.EnsureDir(filepath.Dir(dest), 0700); e != nil {
			return result, e
		}
		if e = rejectSymlink(s.Dir, filepath.Dir(dest)); e != nil {
			return result, e
		}
		if e = os.Rename(f.Name(), dest); e != nil {
			return result, e
		}
		if e = syncDirectory(filepath.Dir(dest)); e != nil {
			return result, e
		}
	}
	sample, e := os.Open(dest)
	if e != nil {
		return result, e
	}
	prefix := make([]byte, 2048+4)
	k, _ := io.ReadFull(sample, prefix)
	sample.Close()
	prefix = prefix[:k]
	media := http.DetectContentType(prefix)
	format := classify(media, m.MIME, m.Name)
	var preview *string
	if strings.HasPrefix(media, "text/") && validator.valid() {
		if len(prefix) > 2048 {
			prefix = prefix[:2048]
			for !utf8.Valid(prefix) {
				prefix = prefix[:len(prefix)-1]
			}
		}
		for !utf8.Valid(prefix) {
			prefix = prefix[:len(prefix)-1]
		}
		v := string(prefix)
		preview = &v
	}
	idBytes := make([]byte, 16)
	if _, e = rand.Read(idBytes); e != nil {
		return result, e
	}
	idBytes[6] = (idBytes[6] & 15) | 64
	idBytes[8] = (idBytes[8] & 63) | 128
	id := fmt.Sprintf("%x-%x-%x-%x-%x", idBytes[:4], idBytes[4:6], idBytes[6:8], idBytes[8:10], idBytes[10:])
	o := Object{1, id, sum, n, media, m.MIME, format, m.Name, m.Title, m.Tags, m.Note, m.Source, label, time.Now(), m.CapturedAt, preview, m.Meta, rel}
	x.Objects = append(x.Objects, o)
	if m.IdempotencyKey != "" {
		x.Keys[key] = id
	}
	if e = s.save(x); e != nil {
		return result, e
	}
	return PutResult{true, o, dedup}, nil
}
func classify(media, declared, name string) string {
	base := strings.Split(media, ";")[0]
	for _, p := range []string{"image", "audio", "video"} {
		if strings.HasPrefix(base, p+"/") {
			return p
		}
	}
	if base == "application/pdf" {
		return "pdf"
	}
	if strings.Contains(base, "zip") || strings.Contains(base, "gzip") || strings.Contains(base, "rar") {
		return "archive"
	}
	if strings.HasPrefix(base, "text/") {
		switch strings.ToLower(filepath.Ext(name)) {
		case ".json":
			return "json"
		case ".yaml", ".yml":
			return "yaml"
		case ".xml":
			return "xml"
		case ".csv":
			return "csv"
		case ".md", ".markdown":
			return "markdown"
		}
		switch strings.Split(declared, ";")[0] {
		case "application/json":
			return "json"
		case "application/yaml", "text/yaml":
			return "yaml"
		case "application/xml", "text/xml":
			return "xml"
		case "text/csv":
			return "csv"
		case "text/markdown":
			return "markdown"
		}
		return "text"
	}
	return "binary"
}

// Visible preserves shared legacy objects without treating a shared caller label as an owner.
func Visible(label, scope string) bool {
	if scope != "" && clientlabel.Shared(scope) {
		return clientlabel.Shared(label) || label == scope
	}
	return clientlabel.Visible(label, scope)
}
func (s *Store) Show(id, scope string) (Object, error) {
	x, e := s.read()
	if e != nil {
		return Object{}, e
	}
	for _, o := range x.Objects {
		if o.ID == id && Visible(o.ClientLabel, scope) {
			return o, nil
		}
	}
	return Object{}, Error("NOT_FOUND", "object not found")
}
func (s *Store) Path(o Object) (string, error) {
	if !hashRE.MatchString(o.SHA256) {
		return "", Error("NOT_FOUND", "invalid blob")
	}
	p, ok := fsutil.ResolveExistingRegularFile(s.Dir, blobPath(o.SHA256))
	if !ok {
		return "", Error("NOT_FOUND", "blob not found")
	}
	return p, nil
}
func (s *Store) Remove(id, scope string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	x, e := s.read()
	if e != nil {
		return e
	}
	found := false
	items := []Object{}
	for _, o := range x.Objects {
		if o.ID == id && Visible(o.ClientLabel, scope) {
			found = true
		} else {
			items = append(items, o)
		}
	}
	if !found {
		return Error("NOT_FOUND", "object not found")
	}
	x.Objects = items
	pruneKeys(&x)
	return s.save(x)
}
func pruneKeys(x *index) {
	ids := map[string]bool{}
	for _, o := range x.Objects {
		ids[o.ID] = true
	}
	for k, id := range x.Keys {
		if !ids[id] {
			delete(x.Keys, k)
		}
	}
}
func (s *Store) retention(x *index) error {
	if s.Config.RetentionDays == nil || *s.Config.RetentionDays <= 0 {
		return nil
	}
	cutoff := time.Now().AddDate(0, 0, -*s.Config.RetentionDays)
	items := []Object{}
	for _, o := range x.Objects {
		if !o.CreatedAt.Before(cutoff) {
			items = append(items, o)
		}
	}
	if len(items) == len(x.Objects) {
		return nil
	}
	x.Objects = items
	pruneKeys(x)
	if e := s.save(*x); e != nil {
		return e
	}
	_, e := s.gc(*x, false)
	return e
}

type GCResult struct {
	OK     bool  `json:"ok"`
	DryRun bool  `json:"dryRun"`
	Files  int   `json:"files"`
	Bytes  int64 `json:"bytes"`
}

func (s *Store) GC(dry bool) (GCResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	x, e := s.read()
	if e != nil {
		return GCResult{}, e
	}
	return s.gc(x, dry)
}
func (s *Store) gc(x index, dry bool) (GCResult, error) {
	result := GCResult{OK: true, DryRun: dry}
	refs := map[string]bool{}
	for _, o := range x.Objects {
		refs[filepath.Clean(filepath.FromSlash(o.BlobPath))] = true
	}
	for _, dir := range []string{"blobs", "tmp"} {
		e := filepath.WalkDir(filepath.Join(s.Dir, dir), func(p string, d os.DirEntry, e error) error {
			if os.IsNotExist(e) {
				return nil
			}
			if e != nil {
				return e
			}
			if d.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(s.Dir, p)
			if refs[rel] {
				return nil
			}
			info, e := d.Info()
			if e != nil {
				return e
			}
			result.Files++
			result.Bytes += info.Size()
			if !dry {
				return os.Remove(p)
			}
			return nil
		})
		if e != nil {
			return result, e
		}
	}
	return result, nil
}
func (s *Store) diskBytes() (int64, error) {
	var n int64
	e := filepath.WalkDir(filepath.Join(s.Dir, "blobs"), func(p string, d os.DirEntry, e error) error {
		if os.IsNotExist(e) {
			return nil
		}
		if e != nil {
			return e
		}
		if !d.IsDir() {
			i, e := d.Info()
			if e != nil {
				return e
			}
			n += i.Size()
		}
		return nil
	})
	return n, e
}

type Usage struct {
	Objects    int            `json:"objects"`
	Bytes      int64          `json:"bytes"`
	QuotaBytes int64          `json:"quotaBytes"`
	Formats    map[string]int `json:"formats"`
}

func (s *Store) Usage() (Usage, error) {
	x, e := s.read()
	if e != nil {
		return Usage{}, e
	}
	u := Usage{Objects: len(x.Objects), QuotaBytes: s.Config.QuotaBytes, Formats: map[string]int{}}
	for _, o := range x.Objects {
		u.Formats[o.Format]++
	}
	u.Bytes, e = s.diskBytes()
	return u, e
}

type Filter struct {
	Type, Source, Client, From, To, Query, Cursor, Scope string
	Tags                                                 []string
	Limit                                                int
}
type Page struct {
	OK         bool     `json:"ok"`
	Items      []Object `json:"items"`
	NextCursor string   `json:"nextCursor,omitempty"`
}

func (s *Store) List(f Filter) (Page, error) {
	p := Page{OK: true, Items: []Object{}}
	if f.Limit == 0 {
		f.Limit = 100
	}
	if f.Limit < 1 || f.Limit > 500 {
		return p, Error("INVALID_PARAMS", "limit must be 1..500")
	}
	var from, to time.Time
	var e error
	for _, v := range []struct {
		s string
		t *time.Time
	}{{f.From, &from}, {f.To, &to}} {
		if v.s != "" {
			*v.t, e = time.Parse(time.RFC3339, v.s)
			if e != nil {
				*v.t, e = time.Parse("2006-01-02", v.s)
			}
			if e != nil {
				return p, Error("INVALID_PARAMS", "invalid time")
			}
		}
	}
	if !from.IsZero() && !to.IsZero() && from.After(to) {
		return p, Error("INVALID_PARAMS", "from is after to")
	}
	x, e := s.read()
	if e != nil {
		return p, e
	}
	sort.Slice(x.Objects, func(i, j int) bool {
		if x.Objects[i].CreatedAt.Equal(x.Objects[j].CreatedAt) {
			return x.Objects[i].ID > x.Objects[j].ID
		}
		return x.Objects[i].CreatedAt.After(x.Objects[j].CreatedAt)
	})
	fingerprint := f
	fingerprint.Cursor = ""
	raw, _ := json.Marshal(fingerprint)
	digest := sha256.Sum256(raw)
	binding := hex.EncodeToString(digest[:])
	after := ""
	if f.Cursor != "" {
		b, e := base64.RawURLEncoding.DecodeString(f.Cursor)
		if e != nil {
			return p, Error("INVALID_PARAMS", "invalid cursor")
		}
		parts := strings.Split(string(b), "|")
		if len(parts) != 2 || parts[0] != binding || !uuidRE.MatchString(parts[1]) {
			return p, Error("INVALID_PARAMS", "cursor does not match filters")
		}
		after = parts[1]
	}
	passed := after == ""
	for _, o := range x.Objects {
		if !Visible(o.ClientLabel, f.Scope) || f.Type != "" && o.Format != f.Type || f.Source != "" && o.Source != f.Source || f.Client != "" && o.ClientLabel != f.Client || !from.IsZero() && o.CreatedAt.Before(from) || !to.IsZero() && o.CreatedAt.After(to) {
			continue
		}
		match := true
		for _, tag := range f.Tags {
			found := false
			for _, v := range o.Tags {
				if v == tag {
					found = true
				}
			}
			if !found {
				match = false
			}
		}
		text := o.Title + "\n" + o.Name + "\n" + strings.Join(o.Tags, "\n") + "\n" + o.Note
		if o.Preview != nil {
			text += "\n" + *o.Preview
		}
		if !match || !strings.Contains(strings.ToLower(text), strings.ToLower(f.Query)) {
			continue
		}
		if !passed {
			if o.ID == after {
				passed = true
			}
			continue
		}
		if len(p.Items) == f.Limit {
			p.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(binding + "|" + p.Items[len(p.Items)-1].ID))
			break
		}
		p.Items = append(p.Items, o)
	}
	if !passed {
		return p, Error("INVALID_PARAMS", "cursor object no longer exists")
	}
	return p, nil
}
func rejectSymlink(root, path string) error {
	for p := path; ; p = filepath.Dir(p) {
		i, e := os.Lstat(p)
		if e != nil {
			return e
		}
		if i.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink in data path")
		}
		if p == root {
			return nil
		}
		if filepath.Dir(p) == p {
			return fmt.Errorf("path outside data root")
		}
	}
}

// utf8Check validates the complete stream, including runes split across copy chunks.
type utf8Check struct {
	tail    []byte
	invalid bool
}

func (v *utf8Check) Write(p []byte) (int, error) {
	n := len(p)
	if v.invalid {
		return n, nil
	}
	b := append(v.tail, p...)
	v.tail = nil
	for len(b) > 0 {
		if !utf8.FullRune(b) {
			v.tail = append([]byte(nil), b...)
			break
		}
		r, size := utf8.DecodeRune(b)
		if r == utf8.RuneError && size == 1 {
			v.invalid = true
			break
		}
		b = b[size:]
	}
	return n, nil
}
func (v *utf8Check) valid() bool { return !v.invalid && len(v.tail) == 0 }
