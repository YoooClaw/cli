package transfer

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var scopeRE = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)
var fullCategories = []string{"agent-settings", "memory", "sessions", "workspace", "skills", "notifications", "recordings", "images", "web-pages", "cron", "agent-backup"}

type FullFile struct {
	Category string `json:"category"`
	Scope    string `json:"scope"`
	Path     string `json:"path"`
	Hash     string `json:"hash"`
	Bytes    int64  `json:"bytes"`
}
type FullSource struct {
	Category     string `json:"category"`
	Scope        string `json:"scope"`
	OriginalRoot string `json:"originalRoot"`
	BackupOnly   bool   `json:"backupOnly,omitempty"`
}
type SeenIndex struct {
	IDs  []string `json:"ids"`
	Keys []string `json:"keys"`
}

func logicalKey(f FullFile) string { return f.Category + ":" + f.Scope + ":" + f.Path }
func validRelative(p string) bool  { return compatibleRelative(p, filepath.Separator == '\\') }
func fullBlob(root string, f FullFile) ([]byte, error) {
	p, e := safeFile(root, "blobs/"+f.Hash)
	if e != nil {
		return nil, e
	}
	return os.ReadFile(p)
}
func readFullPackage(root string, raw []byte, opts ReadOptions) (*Manifest, error) {
	return readFullPackageForPlatform(root, raw, opts, filepath.Separator == '\\')
}

func readFullPackageForPlatform(root string, raw []byte, opts ReadOptions, windows bool) (*Manifest, error) {
	var m Manifest
	if json.Unmarshal(raw, &m) != nil || !uuidRE.MatchString(m.PackageID) || m.Files == nil || m.OriginalBytes < 0 {
		return nil, fail("INVALID_FULL_MANIFEST")
	}
	m.Records = []Record{}
	m.Warnings = []string{}
	m.Seen = map[string]SeenIndex{}
	m.Passive = map[string]bool{}
	consumed := map[string]bool{}
	files := map[string]FullFile{}
	hashes := map[string]int64{}
	accepted := make([]FullFile, 0, len(m.Files))
	for _, f := range m.Files {
		if !contains(fullCategories, f.Category) || !scopeRE.MatchString(f.Scope) || !portableRelative(f.Path) || !hashRE.MatchString(f.Hash) || f.Bytes < 0 {
			return nil, fail("INVALID_FULL_FILE")
		}
		if !compatibleRelative(f.Path, windows) {
			m.Warnings = append(m.Warnings, fmt.Sprintf("UNSUPPORTED_TARGET_PATH category=%q scope=%q path=%q (skipped)", f.Category, f.Scope, f.Path))
			continue
		}
		accepted = append(accepted, f)
		if _, ok := files[logicalKey(f)]; ok {
			return nil, fail("DUPLICATE_FILE")
		}
		files[logicalKey(f)] = f
		if f.Category == "agent-backup" && !strings.HasPrefix(f.Path, "other-agents/"+f.Scope+"/") {
			return nil, fail("UNSAFE_PATH")
		}
		if n, ok := hashes[f.Hash]; ok {
			if n != f.Bytes {
				return nil, fail("CHECKSUM_MISMATCH")
			}
			continue
		}
		p, e := safeFile(root, "blobs/"+f.Hash)
		if e != nil {
			return nil, e
		}
		st, e := os.Stat(p)
		if e != nil || st.Size() != f.Bytes {
			return nil, fail("CHECKSUM_MISMATCH")
		}
		if !opts.SkipBlobHash {
			h, e := hashFileContext(opts.Context, p)
			if e != nil || h != f.Hash {
				return nil, fail("CHECKSUM_MISMATCH")
			}
		}
		hashes[f.Hash] = f.Bytes
	}
	m.Files = accepted
	// Cron definitions are reference files for the destination Agent, not executable input.
	warn := func(f FullFile, location, code string) {
		m.Warnings = append(m.Warnings, fmt.Sprintf("%s:%s: %s (skipped; original retained as reference)", logicalKey(f), location, code))
		m.Passive[logicalKey(f)] = true
	}
	// Adapt business files into the SAME records consumed by the existing merger.
	for _, f := range m.Files {
		if !contains(Types, f.Category) {
			continue
		}
		if f.Category == TypeNotifications && (strings.HasPrefix(f.Path, ".ids/") || strings.HasPrefix(f.Path, ".keys/")) {
			kind := strings.Split(f.Path, "/")[0]
			suffix := kind
			day := strings.TrimSuffix(path.Base(f.Path), suffix)
			if !regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`).MatchString(day) || f.Path != kind+"/"+day+suffix {
				warn(f, "index", "INVALID_NOTIFICATION_INDEX")
				continue
			}
			b, e := fullBlob(root, f)
			if e != nil {
				return nil, e
			}
			consumed[logicalKey(f)] = true
			v := m.Seen[day]
			for lineNo, line := range strings.Split(string(b), "\n") {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				if kind == ".keys" {
					if !hashRE.MatchString(line) {
						warn(f, fmt.Sprintf("line %d", lineNo+1), "INVALID_NOTIFICATION_INDEX")
						continue
					}
					v.Keys = append(v.Keys, line)
				} else {
					v.IDs = append(v.IDs, line)
				}
			}
			m.Seen[day] = v
			continue
		}
		isDay := f.Category == TypeNotifications && dayFileRE.MatchString(f.Path)
		if !isDay && f.Path != "index.json" {
			continue
		}
		b, e := fullBlob(root, f)
		if e != nil {
			return nil, e
		}
		consumed[logicalKey(f)] = true
		var rows []json.RawMessage
		if isDay && strings.HasSuffix(f.Path, ".jsonl") {
			for _, line := range strings.Split(string(b), "\n") {
				rows = append(rows, json.RawMessage(line))
			}
		} else {
			body := b
			if !isDay {
				var index map[string]json.RawMessage
				if json.Unmarshal(b, &index) != nil {
					warn(f, "index", "INVALID_INDEX")
					continue
				}
				body = index[indexKeys[f.Category]]
			}
			if json.Unmarshal(body, &rows) != nil || rows == nil {
				warn(f, "index", "INVALID_INDEX")
				continue
			}
		}
		for rowNo, rawRow := range rows {
			if isDay && strings.HasSuffix(f.Path, ".jsonl") && len(strings.TrimSpace(string(rawRow))) == 0 {
				continue
			}
			var r map[string]any
			if json.Unmarshal(rawRow, &r) != nil || r == nil {
				warn(f, fmt.Sprintf("row %d", rowNo+1), "INVALID_INDEX")
				continue
			}
			used := []string{}
			err := func() error {
				data := portable(f.Category, r)
				if e := validateData(f.Category, data); e != nil {
					return e
				}
				assets := []Asset{}
				for _, field := range assetFields[f.Category] {
					rel, ok := r[field].(string)
					if !ok {
						if r[field] != nil {
							return fail("INVALID_ASSET")
						}
						continue
					}
					if rel == "" {
						continue
					}
					if strings.HasPrefix(rel, "/") {
						found := ""
						for _, source := range m.Sources {
							if source.Category == f.Category && source.Scope == f.Scope && !source.BackupOnly {
								base := strings.TrimSuffix(source.OriginalRoot, "/") + "/"
								if strings.HasPrefix(rel, base) {
									candidate := strings.TrimPrefix(rel, base)
									if found != "" && found != candidate {
										return fail("AMBIGUOUS_SOURCE")
									}
									found = candidate
								}
							}
						}
						if found == "" {
							return fail("REFERENCE_OUTSIDE_SOURCE")
						}
						rel = found
					}
					if !portableRelative(rel) {
						return fail("UNSAFE_PATH")
					}
					a, ok := files[f.Category+":"+f.Scope+":"+rel]
					if !ok {
						return fail("REFERENCED_FILE_MISSING")
					}
					used = append(used, logicalKey(a))
					ext := strings.ToLower(path.Ext(rel))
					if !extensionRE.MatchString(ext) {
						ext = ".bin"
					}
					assets = append(assets, Asset{Field: field, Hash: a.Hash, Bytes: a.Bytes, Extension: ext})
				}
				if f.Category == TypeWebPages && !hasField(assets, "relativePath") {
					return fail("MISSING_BODY")
				}
				origin := m.PackageID
				if mark, ok := r["transfer"].(map[string]any); ok {
					if o, ok := mark["origin"].(string); ok && uuidRE.MatchString(o) {
						origin = o
					}
				}
				m.Records = append(m.Records, Record{Type: f.Category, Data: data, Assets: assets, RecordID: identity(f.Category, data), RecordVersion: recordVersion(data, assets), RecordSchemaVersion: 1, Origin: origin})
				return nil
			}()
			if err != nil {
				switch ErrorCode(err) {
				case "INVALID_INDEX", "INVALID_RECORD_DATA", "INVALID_RECORD_ID", "INVALID_NOTIFICATION", "INVALID_METADATA", "INVALID_WEB_PAGE", "INVALID_ASSET", "REFERENCED_FILE_MISSING", "MISSING_BODY":
					warn(f, fmt.Sprintf("row %d", rowNo+1), ErrorCode(err))
					continue
				default:
					return nil, err
				}
			}
			for _, key := range used {
				consumed[key] = true
			}
		}
	}
	for _, f := range m.Files {
		if isBusinessFile(f) && !consumed[logicalKey(f)] {
			m.Passive[logicalKey(f)] = true
		}
	}
	// Do not hide conflicting duplicate business identities within a full package.
	records := map[string]string{}
	for _, r := range m.Records {
		key := r.Type + ":" + r.RecordID
		if old, ok := records[key]; ok && old != r.RecordVersion {
			return nil, fail("DUPLICATE_RECORD")
		}
		records[key] = r.RecordVersion
	}
	m.Records = dedupeRecords(m.Records)
	sort.Slice(m.Files, func(i, j int) bool { return logicalKey(m.Files[i]) < logicalKey(m.Files[j]) })
	return &m, nil
}
func blobHashes(m *Manifest) []string {
	set := map[string]bool{}
	for _, f := range m.Files {
		set[f.Hash] = true
	}
	for _, r := range m.Records {
		for _, a := range r.Assets {
			set[a.Hash] = true
		}
	}
	out := make([]string, 0, len(set))
	for h := range set {
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}
func isBusinessFile(f FullFile) bool { return contains(Types, f.Category) }
