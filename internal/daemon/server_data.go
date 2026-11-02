package daemon

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/YoooClaw/cli/internal/data"
	"github.com/YoooClaw/cli/internal/errs"
)

func writeDataError(w http.ResponseWriter, e error) {
	code := "YOOOCLAW_DATA_WRITE_FAILED"
	message := "data operation failed"
	status := 500
	var de *errs.Error
	if errors.As(e, &de) {
		code = de.Code
		message = de.Message
	}
	switch strings.TrimPrefix(code, "YOOOCLAW_DATA_") {
	case "NOT_FOUND":
		status = 404
	case "INVALID_META", "INVALID_PARAMS":
		status = 400
	case "TOO_LARGE", "QUOTA_EXCEEDED":
		status = 413
	case "IDEMPOTENCY_CONFLICT":
		status = 409
	}
	writeJSON(w, status, map[string]any{"ok": false, "error": map[string]any{"code": code, "message": message}})
}
func (s *server) dataStore() *data.Store { return data.New(s.ctx.Paths.Data, s.cfg.Data) }
func (s *server) dataStatus() any {
	u, e := s.dataStore().Usage()
	if e != nil {
		return map[string]any{"error": "data index unavailable"}
	}
	return u
}
func dataFilter(r *http.Request) (data.Filter, error) {
	q := r.URL.Query()
	f := data.Filter{Type: q.Get("type"), Source: q.Get("source"), Client: q.Get("client"), From: q.Get("from"), To: q.Get("to"), Query: q.Get("query"), Cursor: q.Get("cursor"), Tags: q["tag"]}
	if q.Has("limit") {
		n, e := strconv.Atoi(q.Get("limit"))
		if e != nil || n < 1 {
			return f, data.Error("INVALID_PARAMS", "invalid limit")
		}
		f.Limit = n
	}
	return f, nil
}
func (s *server) handleData(w http.ResponseWriter, r *http.Request, a authResult) {
	store := s.dataStore()
	path := r.URL.Path
	scope := a.scope()
	// Relay v1 is write-only, including requests forwarded as HTTP paths.
	if a.authKind == "relay-api-key" && path != "/gateway/data.write" {
		writeDataError(w, data.Error("NOT_FOUND", "endpoint not available over Relay"))
		return
	}
	if path == "/gateway/data.write" && r.Method == http.MethodPost {
		var body struct {
			data.Metadata
			ContentBase64 *string `json:"contentBase64"`
		}
		limit := store.Config.MaxInlineBytes
		maxBody := base64.StdEncoding.EncodedLen(int(limit)) + 128*1024
		b, e := io.ReadAll(io.LimitReader(r.Body, int64(maxBody)+1))
		if e != nil || len(b) > maxBody {
			writeDataError(w, data.Error("TOO_LARGE", "inline request too large"))
			return
		}
		if json.Unmarshal(b, &body) != nil || body.ContentBase64 == nil {
			writeDataError(w, data.Error("INVALID_PARAMS", "contentBase64 is required"))
			return
		}
		if len(*body.ContentBase64) > base64.StdEncoding.EncodedLen(int(limit)) {
			writeDataError(w, data.Error("TOO_LARGE", "inline content too large"))
			return
		}
		raw, e := base64.StdEncoding.Strict().DecodeString(*body.ContentBase64)
		if e != nil {
			writeDataError(w, data.Error("INVALID_PARAMS", "invalid base64"))
			return
		}
		if int64(len(raw)) > limit {
			writeDataError(w, data.Error("TOO_LARGE", "inline content too large"))
			return
		}
		if body.Source == "" {
			body.Source = "relay"
		}
		result, e := store.Put(strings.NewReader(string(raw)), body.Metadata, a.clientLabel)
		if e != nil {
			writeDataError(w, e)
			return
		}
		writeJSON(w, 201, result)
		return
	}
	if path == "/data" {
		switch r.Method {
		case http.MethodPost:
			q := r.URL.Query()
			m := data.Metadata{Name: q.Get("name"), Title: q.Get("title"), MIME: r.Header.Get("Content-Type"), Tags: q["tag"], Source: q.Get("source"), Note: q.Get("note"), Meta: json.RawMessage(q.Get("meta")), IdempotencyKey: r.Header.Get("Idempotency-Key")}
			if m.Source == "" {
				m.Source = "http"
			}
			if v := q.Get("capturedAt"); v != "" {
				t, e := time.Parse(time.RFC3339, v)
				if e != nil {
					writeDataError(w, data.Error("INVALID_PARAMS", "invalid capturedAt"))
					return
				}
				m.CapturedAt = &t
			}
			result, e := store.Put(r.Body, m, a.clientLabel)
			if e != nil {
				writeDataError(w, e)
				return
			}
			writeJSON(w, 201, result)
			return
		case http.MethodGet:
			f, e := dataFilter(r)
			if e != nil {
				writeDataError(w, e)
				return
			}
			f.Scope = scope
			result, e := store.List(f)
			if e != nil {
				writeDataError(w, e)
				return
			}
			writeJSON(w, 200, result)
			return
		}
	}
	if path == "/data/gc" && r.Method == http.MethodPost {
		if scope != "" {
			writeDataError(w, data.Error("NOT_FOUND", "endpoint not found"))
			return
		}
		result, e := store.GC(r.URL.Query().Get("dryRun") == "true")
		if e != nil {
			writeDataError(w, e)
			return
		}
		writeJSON(w, 200, result)
		return
	}
	rest := strings.TrimPrefix(path, "/data/")
	parts := strings.Split(rest, "/")
	if len(parts) > 2 {
		writeDataError(w, data.Error("NOT_FOUND", "object not found"))
		return
	}
	o, e := store.Show(parts[0], scope)
	if e != nil {
		writeDataError(w, e)
		return
	}
	if len(parts) == 2 && parts[1] == "content" && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
		p, e := store.Path(o)
		if e != nil {
			writeDataError(w, e)
			return
		}
		f, e := os.Open(p)
		if e != nil {
			writeDataError(w, e)
			return
		}
		defer f.Close()
		name := o.Name
		if name == "" {
			name = o.ID
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "sandbox")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Length", strconv.FormatInt(o.SizeBytes, 10))
		w.WriteHeader(200)
		if r.Method == http.MethodGet {
			io.Copy(w, f)
		}
		return
	}
	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, 200, map[string]any{"ok": true, "data": o})
			return
		case http.MethodDelete:
			if e := store.Remove(o.ID, scope); e != nil {
				writeDataError(w, e)
				return
			}
			writeJSON(w, 200, map[string]any{"ok": true, "id": o.ID})
			return
		}
	}
	writeDataError(w, data.Error("NOT_FOUND", "endpoint not found"))
}
