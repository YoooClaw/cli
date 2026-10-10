// Package nanoshell stores immutable, client-scoped device installation packages.
package nanoshell

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const MaxPackageBytes = 128 * 1024
const MaxExpandedBytes = 256 * 1024

var hashRE = regexp.MustCompile(`^[0-9a-f]{64}$`)
var appRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string    { return e.Message }
func fail(code, msg string) error { return &Error{code, msg} }
func Code(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return "INTERNAL_ERROR"
}

type Item struct {
	AppID        string `json:"appId"`
	Name         string `json:"name"`
	Version      int    `json:"version"`
	PackageID    string `json:"packageId"`
	PackageBytes int    `json:"packageBytes"`
	PublishedAt  string `json:"publishedAt"`
}
type record struct {
	Item
	ClientLabel string `json:"clientLabel"`
	FileName    string `json:"fileName"`
}
type Manifest struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version int    `json:"version"`
	Entry   string `json:"entry"`
}
type ListResult struct {
	Items      []Item `json:"items"`
	NextCursor string `json:"nextCursor"`
}
type DownloadResult struct {
	AppID       string `json:"appId"`
	Version     int    `json:"version"`
	PackageID   string `json:"packageId"`
	FileName    string `json:"fileName"`
	ContentType string `json:"contentType"`
	Encoding    string `json:"encoding"`
	Size        int    `json:"size"`
	Data        string `json:"data"`
}
type PublishResult struct {
	Item
	Duplicated bool `json:"duplicated"`
}
type Store struct{ Root string }

func digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func recordID(label, app string, version int) string {
	return "zip-" + digest([]byte(fmt.Sprintf("%s\x00%s\x00%d", label, app, version)))
}

// readRegular never reads an unbounded or symlinked input.
func readRegular(p string, max int) ([]byte, error) {
	st, err := os.Lstat(p)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular file")
	}
	if st.Size() > int64(max) {
		return nil, fail("PACKAGE_TOO_LARGE", "package exceeds size limit")
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, int64(max)+1))
	if err != nil {
		return nil, err
	}
	if len(b) > max {
		return nil, fail("PACKAGE_TOO_LARGE", "package exceeds size limit")
	}
	return b, nil
}

// Validate validates a single .nsp directory without extracting untrusted ZIP paths.
func Validate(raw []byte) (Manifest, string, error) {
	var m Manifest
	bad := func() (Manifest, string, error) {
		return m, "", fail("PACKAGE_CORRUPTED", "invalid device ZIP package")
	}
	if len(raw) > MaxPackageBytes {
		return m, "", fail("PACKAGE_TOO_LARGE", "ZIP exceeds 128 KiB")
	}
	z, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return bad()
	}
	if len(z.File) < 2 || len(z.File) > 4 {
		return bad()
	}
	files := map[string][]byte{}
	dir := ""
	total := 0
	for _, f := range z.File {
		n := f.Name
		if strings.Contains(n, "\\") || strings.HasPrefix(n, "/") || strings.Contains(n, "\x00") {
			return bad()
		}
		parts := strings.Split(strings.TrimSuffix(n, "/"), "/")
		if len(parts) < 1 || len(parts) > 2 || !strings.HasSuffix(parts[0], ".nsp") || !appRE.MatchString(parts[0]) {
			return bad()
		}
		if dir != "" && dir != parts[0] {
			return bad()
		}
		dir = parts[0]
		if f.Mode()&os.ModeSymlink != 0 {
			return bad()
		}
		if f.FileInfo().IsDir() {
			if len(parts) != 1 {
				return bad()
			}
			continue
		}
		if !f.Mode().IsRegular() || len(parts) != 2 {
			return bad()
		}
		name := parts[1]
		if name != "manifest.json" && name != "app.wasm" && name != "README-INSTALL.txt" {
			return bad()
		}
		if _, ok := files[name]; ok {
			return bad()
		}
		if f.UncompressedSize64 > MaxExpandedBytes {
			return bad()
		}
		r, e := f.Open()
		if e != nil {
			return bad()
		}
		b, e := io.ReadAll(io.LimitReader(r, MaxExpandedBytes+1))
		r.Close()
		total += len(b)
		if e != nil || total > MaxExpandedBytes {
			return bad()
		}
		files[name] = b
	}
	if json.Unmarshal(files["manifest.json"], &m) != nil || !appRE.MatchString(m.ID) || strings.TrimSpace(m.Name) == "" || len(m.Name) > 256 || m.Version < 1 || m.Entry != "app.wasm" {
		return bad()
	}
	wasm := files["app.wasm"]
	if bytes.HasPrefix(wasm, []byte("NSP1")) {
		if len(wasm) < 16 || int(binary.LittleEndian.Uint32(wasm[8:12])) != len(wasm)-16 {
			return bad()
		}
		wasm = wasm[16:]
	}
	if len(wasm) < 8 || len(wasm) > 12288 || !bytes.Equal(wasm[:8], []byte{0, 97, 115, 109, 1, 0, 0, 0}) {
		return bad()
	}
	return m, strings.TrimSuffix(dir, ".nsp") + ".zip", nil
}

func (s Store) records(scope string) ([]record, error) {
	entries, err := os.ReadDir(s.Root)
	if os.IsNotExist(err) {
		return []record{}, nil
	}
	if err != nil {
		return nil, fail("STORAGE_UNAVAILABLE", "cannot read package store")
	}
	out := []record{}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".publish-") {
			continue
		}
		// Legacy program-only records remain untouched and are not advertised as ZIPs.
		if !strings.HasPrefix(e.Name(), "zip-") || !hashRE.MatchString(strings.TrimPrefix(e.Name(), "zip-")) || !e.IsDir() {
			continue
		}
		raw, err := readRegular(filepath.Join(s.Root, e.Name(), "metadata.json"), 16384)
		var r record
		if err != nil || json.Unmarshal(raw, &r) != nil || !hashRE.MatchString(r.PackageID) || r.ClientLabel == "" || !appRE.MatchString(r.AppID) || r.Version < 1 || recordID(r.ClientLabel, r.AppID, r.Version) != e.Name() {
			return nil, fail("STORAGE_UNAVAILABLE", "invalid package index")
		}
		if scope == "" || r.ClientLabel == scope {
			out = append(out, r)
		}
	}
	return out, nil
}

// Publish atomically publishes one immutable app version. Independent CLI processes
// stage separately; rename into an existing nonempty version directory cannot replace it.
func (s Store) Publish(file, label string) (PublishResult, error) {
	var result PublishResult
	label = strings.TrimSpace(label)
	if label == "" || label == "all" || len(label) > 256 || strings.ContainsRune(label, 0) {
		return result, fail("INVALID_PARAMS", "an explicit client label is required")
	}
	raw, err := readRegular(file, MaxPackageBytes)
	if err != nil {
		return result, err
	}
	m, name, err := Validate(raw)
	if err != nil {
		return result, err
	}
	r := record{Item: Item{m.ID, m.Name, m.Version, digest(raw), len(raw), time.Now().UTC().Format("2006-01-02T15:04:05.000000000Z")}, ClientLabel: label, FileName: name}
	if err = os.MkdirAll(s.Root, 0700); err != nil {
		return result, fail("STORAGE_UNAVAILABLE", "cannot create package store")
	}
	dest := filepath.Join(s.Root, recordID(label, m.ID, m.Version))
	existing := func() (PublishResult, error) {
		b, e := readRegular(filepath.Join(dest, "metadata.json"), 16384)
		var old record
		if e != nil || json.Unmarshal(b, &old) != nil {
			return result, fail("STORAGE_UNAVAILABLE", "cannot read published version")
		}
		if old.PackageID != r.PackageID {
			return result, fail("VERSION_CONFLICT", "this app version already has a different package; increase version")
		}
		b, e = readRegular(filepath.Join(dest, "package.zip"), MaxPackageBytes)
		if e != nil || digest(b) != old.PackageID {
			return result, fail("PACKAGE_CORRUPTED", "published package is corrupted")
		}
		return PublishResult{old.Item, true}, nil
	}
	if _, err = os.Lstat(dest); err == nil {
		return existing()
	} else if !os.IsNotExist(err) {
		return result, err
	}
	stage, err := os.MkdirTemp(s.Root, ".publish-")
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(stage)
	meta, _ := json.Marshal(r)
	if err = os.WriteFile(filepath.Join(stage, "package.zip"), raw, 0600); err != nil {
		return result, err
	}
	if err = os.WriteFile(filepath.Join(stage, "metadata.json"), meta, 0600); err != nil {
		return result, err
	}
	if err = os.Rename(stage, dest); err != nil {
		if _, e := os.Stat(dest); e == nil {
			return existing()
		}
		return result, fail("STORAGE_UNAVAILABLE", "cannot publish package")
	}
	return PublishResult{r.Item, false}, nil
}

type cursor struct {
	Scope string `json:"scope"`
	Time  string `json:"time"`
	App   string `json:"app"`
}

func (s Store) List(scope string, limit int, token string) (ListResult, error) {
	result := ListResult{Items: []Item{}}
	if limit < 1 || limit > 100 {
		return result, fail("INVALID_PARAMS", "limit must be between 1 and 100")
	}
	var c cursor
	if token != "" {
		b, e := base64.RawURLEncoding.DecodeString(token)
		if len(token) > 2048 || e != nil || json.Unmarshal(b, &c) != nil || c.Scope != scope || c.Time == "" || !appRE.MatchString(c.App) {
			return result, fail("INVALID_CURSOR", "invalid cursor")
		}
	}
	records, err := s.records(scope)
	if err != nil {
		return result, err
	}
	latest := map[string]Item{}
	for _, r := range records {
		v, ok := latest[r.AppID]
		if !ok || r.Version > v.Version || r.Version == v.Version && r.PublishedAt > v.PublishedAt {
			latest[r.AppID] = r.Item
		}
	}
	items := []Item{}
	for _, r := range latest {
		items = append(items, r)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].PublishedAt != items[j].PublishedAt {
			return items[i].PublishedAt > items[j].PublishedAt
		}
		return items[i].AppID < items[j].AppID
	})
	for _, r := range items {
		if token != "" && (r.PublishedAt > c.Time || r.PublishedAt == c.Time && r.AppID <= c.App) {
			continue
		}
		if len(result.Items) == limit {
			last := result.Items[len(result.Items)-1]
			b, _ := json.Marshal(cursor{scope, last.PublishedAt, last.AppID})
			result.NextCursor = base64.RawURLEncoding.EncodeToString(b)
			break
		}
		result.Items = append(result.Items, r)
	}
	return result, nil
}
func (s Store) Download(scope, id string) (DownloadResult, error) {
	var out DownloadResult
	if !hashRE.MatchString(id) {
		return out, fail("INVALID_PARAMS", "packageId must be a lowercase SHA-256")
	}
	records, err := s.records(scope)
	if err != nil {
		return out, err
	}
	for _, r := range records {
		if r.PackageID != id {
			continue
		}
		b, e := readRegular(filepath.Join(s.Root, recordID(r.ClientLabel, r.AppID, r.Version), "package.zip"), MaxPackageBytes)
		if e != nil {
			if Code(e) == "PACKAGE_TOO_LARGE" {
				return out, e
			}
			return out, fail("PACKAGE_CORRUPTED", "published package is missing or unreadable")
		}
		if len(b) != r.PackageBytes || digest(b) != id {
			return out, fail("PACKAGE_CORRUPTED", "package checksum mismatch")
		}
		m, _, e := Validate(b)
		if e != nil {
			return out, e
		}
		if m.ID != r.AppID || m.Version != r.Version {
			return out, fail("PACKAGE_CORRUPTED", "package metadata mismatch")
		}
		return DownloadResult{r.AppID, r.Version, id, path.Base(r.FileName), "application/zip", "base64", len(b), base64.StdEncoding.EncodeToString(b)}, nil
	}
	return out, fail("PACKAGE_NOT_FOUND", "package not found")
}
