package transfer

import (
	"encoding/json"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
)

const MaxFullBytes int64 = 10_000_000_000

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
func validRelative(p string) bool {
	if p == "" || len(p) > 4096 || strings.ContainsAny(p, "\\\x00\r\n\t") || strings.HasPrefix(p, "/") {
		return false
	}
	for _, c := range strings.Split(p, "/") {
		if c == "" || c == "." || c == ".." {
			return false
		}
	}
	return true
}
func fullBlob(root string, f FullFile) ([]byte, error) {
	if f.Bytes > MaxManifestBytes {
		return nil, fail("INDEX_TOO_LARGE")
	}
	p, e := safeFile(root, "blobs/"+f.Hash)
	if e != nil {
		return nil, e
	}
	return os.ReadFile(p)
}
func readFullPackage(root string, raw []byte, opts ReadOptions) (*Manifest, error) {
	var m Manifest
	if json.Unmarshal(raw, &m) != nil || !uuidRE.MatchString(m.PackageID) || m.Files == nil || len(m.Files) > MaxRecords || m.OriginalBytes < 0 || m.OriginalBytes > MaxFullBytes {
		return nil, fail("INVALID_FULL_MANIFEST")
	}
	m.Records = []Record{}
	m.Warnings = []string{}
	m.Seen = map[string]SeenIndex{}
	m.Passive = map[string]bool{}
	consumed := map[string]bool{}
	files := map[string]FullFile{}
	hashes := map[string]int64{}
	var total int64
	for _, f := range m.Files {
		if !contains(fullCategories, f.Category) || !scopeRE.MatchString(f.Scope) || !validRelative(f.Path) || !hashRE.MatchString(f.Hash) || f.Bytes < 0 || f.Bytes > MaxFullBytes {
			return nil, fail("INVALID_FULL_FILE")
		}
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
		total += f.Bytes
		if total > MaxFullBytes {
			return nil, fail("ORIGINAL_SIZE_EXCEEDED")
		}
	}
	for _, f := range m.Files {
		if f.Category == "cron" {
			b, e := fullBlob(root, f)
			if e != nil {
				return nil, e
			}
			var cron struct {
				Jobs []map[string]any `json:"jobs"`
			}
			if json.Unmarshal(b, &cron) != nil || cron.Jobs == nil {
				return nil, fail("INVALID_CRON_INDEX")
			}
			ids := map[string]bool{}
			for _, job := range cron.Jobs {
				id, ok := job["id"].(string)
				if !ok || id == "" || ids[id] || job["enabled"] != true {
					return nil, fail("INVALID_CRON_INDEX")
				}
				if _, ok := job["schedule"].(map[string]any); !ok {
					return nil, fail("INVALID_CRON_INDEX")
				}
				if _, ok := job["payload"].(map[string]any); !ok {
					return nil, fail("INVALID_CRON_INDEX")
				}
				ids[id] = true
			}
		}
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
				return nil, fail("INVALID_NOTIFICATION_INDEX")
			}
			b, e := fullBlob(root, f)
			if e != nil {
				return nil, e
			}
			consumed[logicalKey(f)] = true
			v := m.Seen[day]
			for _, line := range strings.Split(string(b), "\n") {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				if len(line) > 4096 {
					return nil, fail("INVALID_NOTIFICATION_INDEX")
				}
				if kind == ".keys" {
					if !hashRE.MatchString(line) {
						return nil, fail("INVALID_NOTIFICATION_INDEX")
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
		var rows []map[string]any
		if isDay {
			if strings.HasSuffix(f.Path, ".jsonl") {
				for _, line := range strings.Split(string(b), "\n") {
					if strings.TrimSpace(line) == "" {
						continue
					}
					var r map[string]any
					if json.Unmarshal([]byte(line), &r) != nil || r == nil {
						return nil, fail("INVALID_INDEX")
					}
					rows = append(rows, r)
				}
			} else if json.Unmarshal(b, &rows) != nil {
				return nil, fail("INVALID_INDEX")
			}
		} else {
			var index map[string]json.RawMessage
			if json.Unmarshal(b, &index) != nil || json.Unmarshal(index[indexKeys[f.Category]], &rows) != nil {
				return nil, fail("INVALID_INDEX")
			}
		}
		for _, r := range rows {
			if r == nil {
				return nil, fail("INVALID_INDEX")
			}
			data := portable(f.Category, r)
			if e := validateData(f.Category, data); e != nil {
				return nil, e
			}
			assets := []Asset{}
			for _, field := range assetFields[f.Category] {
				rel, ok := r[field].(string)
				if !ok {
					if r[field] != nil {
						return nil, fail("INVALID_ASSET")
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
									return nil, fail("AMBIGUOUS_SOURCE")
								}
								found = candidate
							}
						}
					}
					if found == "" {
						return nil, fail("REFERENCE_OUTSIDE_SOURCE")
					}
					rel = found
				}
				if !validRelative(rel) {
					return nil, fail("UNSAFE_PATH")
				}
				a, ok := files[f.Category+":"+f.Scope+":"+rel]
				if !ok {
					return nil, fail("REFERENCED_FILE_MISSING")
				}
				consumed[logicalKey(a)] = true
				ext := strings.ToLower(path.Ext(rel))
				if !extensionRE.MatchString(ext) {
					ext = ".bin"
				}
				assets = append(assets, Asset{Field: field, Hash: a.Hash, Bytes: a.Bytes, Extension: ext})
			}
			if f.Category == TypeWebPages && !hasField(assets, "relativePath") {
				return nil, fail("MISSING_BODY")
			}
			origin := m.PackageID
			if mark, ok := r["transfer"].(map[string]any); ok {
				if o, ok := mark["origin"].(string); ok && uuidRE.MatchString(o) {
					origin = o
				}
			}
			m.Records = append(m.Records, Record{Type: f.Category, Data: data, Assets: assets, RecordID: identity(f.Category, data), RecordVersion: recordVersion(data, assets), RecordSchemaVersion: 1, Origin: origin})
			if len(m.Records) > MaxRecords {
				return nil, fail("PACKAGE_TOO_MANY_RECORDS")
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
