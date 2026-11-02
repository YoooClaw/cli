package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/YoooClaw/cli/internal/config"
	"github.com/YoooClaw/cli/internal/data"
	"github.com/YoooClaw/cli/internal/transfercloud"
)

func dataImportPath(path string) bool {
	return path == "/data/import" || strings.HasPrefix(path, "/data/imports/") || path == "/gateway/data.import" || path == "/gateway/data.import.status" || path == "/gateway/data.import.retry"
}
func (s *server) dataImportKey(label string) string {
	set := s.snapshotCreds()
	for _, e := range set.Entries {
		if e.Label == label {
			return e.Key
		}
	}
	if label == "local" && set.DefaultEntry != nil {
		return set.DefaultEntry.Key
	}
	return ""
}
func (s *server) dataImportIdentity(label string) string {
	key := s.dataImportKey(label)
	if label != "local" && key == "" {
		return ""
	}
	h := sha256.Sum256([]byte(label + "\x00" + key + "\x00" + config.ResolveCloudHost(s.cfg)))
	return hex.EncodeToString(h[:])
}
func (s *server) startDataImports() error {
	client := data.NewImportHTTPClient()
	service, e := data.NewImports(s.dataStore(), data.ImportDependencies{
		Identity: s.dataImportIdentity,
		Refresh: func(ctx context.Context, label, id string) (string, error) {
			key := s.dataImportKey(label)
			if key == "" {
				return "", data.Error("IMPORT_UNAVAILABLE", "API key required to refresh OSS task")
			}
			return transfercloud.New(key, config.ResolveCloudHost(s.cfg)).FileDownloadURL(ctx, id)
		},
		Download: func(ctx context.Context, rawURL, key, dest, checkpoint string, size int64) error {
			return data.DownloadImport(ctx, client, rawURL, key, dest, checkpoint, size)
		},
		Notify: func(label string, status data.ImportStatus) {
			if e := s.currentEgress(); e != nil {
				_ = e.PushEventTo(label, "data.import.status", status)
			}
		},
	})
	if e != nil {
		return e
	}
	s.dataImports = service
	return nil
}
func decodeDataImport(w http.ResponseWriter, r *http.Request, out any) bool {
	b, e := io.ReadAll(io.LimitReader(r.Body, (64<<10)+1))
	if e != nil || len(b) > 64<<10 {
		writeDataError(w, data.Error("TOO_LARGE", "import metadata exceeds 64 KiB"))
		return false
	}
	if json.Unmarshal(b, out) != nil {
		writeDataError(w, data.Error("INVALID_PARAMS", "invalid import request"))
		return false
	}
	return true
}
func (s *server) handleDataImport(w http.ResponseWriter, r *http.Request, a authResult) {
	w.Header().Set("Cache-Control", "no-store")
	if s.dataImports == nil {
		writeDataError(w, data.Error("IMPORT_UNAVAILABLE", "data import service unavailable"))
		return
	}
	path := r.URL.Path
	if (path == "/data/import" || path == "/gateway/data.import") && r.Method == http.MethodPost {
		var body data.ImportRequest
		if !decodeDataImport(w, r, &body) {
			return
		}
		if body.Source == "" {
			body.Source = "oss"
		}
		result, e := s.dataImports.Accept(body, a.clientLabel)
		if e != nil {
			writeDataError(w, e)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "data": result})
		return
	}
	var body struct {
		TaskID string `json:"taskId"`
		URL    string `json:"url"`
	}
	retry := false
	if strings.HasPrefix(path, "/gateway/") {
		if r.Method != http.MethodPost {
			writeDataError(w, data.Error("NOT_FOUND", "endpoint not found"))
			return
		}
		if !decodeDataImport(w, r, &body) {
			return
		}
		retry = path == "/gateway/data.import.retry"
	} else {
		parts := strings.Split(strings.TrimPrefix(path, "/data/imports/"), "/")
		if len(parts) == 1 && r.Method == http.MethodGet {
			body.TaskID = parts[0]
		} else if len(parts) == 2 && parts[1] == "retry" && r.Method == http.MethodPost {
			if !decodeDataImport(w, r, &body) {
				return
			}
			body.TaskID = parts[0]
			retry = true
		} else {
			writeDataError(w, data.Error("NOT_FOUND", "endpoint not found"))
			return
		}
	}
	var result data.ImportStatus
	var e error
	if retry {
		result, e = s.dataImports.Retry(body.TaskID, a.scope(), body.URL)
	} else {
		result, e = s.dataImports.Status(body.TaskID, a.scope())
	}
	if e != nil {
		writeDataError(w, e)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "data": result})
}
