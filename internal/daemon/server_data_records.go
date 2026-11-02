package daemon

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/YoooClaw/cli/internal/data"
)

var dataRecordMethods = []string{"data.capabilities", "data.list", "data.get", "data.content", "data.resolve", "data.upload.begin", "data.upload.chunk", "data.upload.status", "data.upload.commit", "data.upload.abort"}

func dataRecordPath(path string) bool {
	for _, method := range dataRecordMethods {
		if path == "/gateway/"+method {
			return true
		}
	}
	return false
}

// Both raw RPC bodies and messageBridge's POST envelope are supported. Bound the
// outer frame before decoding; the inner operation cannot change HTTP method.
func decodeDataBody(w http.ResponseWriter, r *http.Request, out any, limit int64) bool {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 2*limit+1025))
	if err != nil || int64(len(raw)) > 2*limit+1024 {
		writeDataError(w, data.Error("TOO_LARGE", "data request too large"))
		return false
	}
	var probe map[string]json.RawMessage
	if json.Unmarshal(raw, &probe) != nil {
		writeDataError(w, data.Error("INVALID_PARAMS", "invalid JSON request"))
		return false
	}
	if method, ok := probe["method"]; ok {
		var m string
		if len(probe) != 2 || json.Unmarshal(method, &m) != nil || m != "POST" {
			writeDataError(w, data.Error("INVALID_PARAMS", "expected POST envelope"))
			return false
		}
		var body string
		if json.Unmarshal(probe["body"], &body) != nil {
			writeDataError(w, data.Error("INVALID_PARAMS", "envelope body must be a JSON string"))
			return false
		}
		raw = []byte(body)
	}
	if int64(len(raw)) > limit {
		writeDataError(w, data.Error("TOO_LARGE", "data request too large"))
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err = dec.Decode(out); err != nil || dec.Decode(new(any)) != io.EOF || string(bytes.TrimSpace(raw)) == "null" {
		writeDataError(w, data.Error("INVALID_PARAMS", "invalid data request fields"))
		return false
	}
	return true
}

// Public projections retain display metadata but never expose daemon filesystem paths.
func publicDataObject(o data.Object) map[string]json.RawMessage {
	b, _ := json.Marshal(o)
	var m map[string]json.RawMessage
	_ = json.Unmarshal(b, &m)
	delete(m, "blobPath")
	return m
}
func (s *server) handleDataRecords(w http.ResponseWriter, r *http.Request, a authResult) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		writeDataError(w, data.Error("NOT_FOUND", "endpoint not found"))
		return
	}
	store := s.dataStore()
	method := strings.TrimPrefix(r.URL.Path, "/gateway/")
	respond := func(v any) { writeJSON(w, 200, map[string]any{"ok": true, "data": v}) }
	// Remote reads are strictly client-owned, including historical shared labels.
	owned := func(id string) (data.Object, error) {
		o, e := store.Show(id, a.scope())
		if e == nil && a.scope() != "" && o.ClientLabel != a.clientLabel {
			e = data.Error("NOT_FOUND", "object not found")
		}
		return o, e
	}
	switch method {
	case "data.capabilities":
		var body struct{}
		if !decodeDataBody(w, r, &body, 4096) {
			return
		}
		respond(map[string]any{"protocol": "data-records/1", "methods": append(append([]string{}, dataRecordMethods...), "data.write", "data.import", "data.import.status", "data.import.retry"), "maxInlineBytes": min(store.Config.MaxInlineBytes, int64(data.ChunkBytes)), "maxChunkBytes": data.ChunkBytes, "maxUploadBytes": store.Config.MaxImportBytes, "maxImportBytes": store.Config.MaxImportBytes, "quotaBytes": store.Config.QuotaBytes, "maxPendingUploads": 32, "maxListLimit": 50, "scope": "client", "ossImportAvailable": s.dataImports != nil})
	case "data.list":
		var b struct {
			Source string   `json:"source"`
			Type   string   `json:"type"`
			From   string   `json:"from"`
			To     string   `json:"to"`
			Query  string   `json:"query"`
			Tags   []string `json:"tags"`
			Cursor string   `json:"cursor"`
			Limit  int      `json:"limit"`
		}
		if !decodeDataBody(w, r, &b, 64<<10) {
			return
		}
		if b.Limit == 0 {
			b.Limit = 50
		}
		if b.Limit < 1 || b.Limit > 50 {
			writeDataError(w, data.Error("INVALID_PARAMS", "limit must be 1..50"))
			return
		}
		f := data.Filter{Source: b.Source, Type: b.Type, From: b.From, To: b.To, Query: b.Query, Tags: b.Tags, Cursor: b.Cursor, Limit: b.Limit, Scope: a.scope()}
		if a.scope() != "" {
			f.Client = a.clientLabel
		}
		p, e := store.List(f)
		if e != nil {
			writeDataError(w, e)
			return
		}
		items := make([]map[string]json.RawMessage, 0, len(p.Items))
		for _, o := range p.Items {
			items = append(items, publicDataObject(o))
		}
		respond(map[string]any{"items": items, "nextCursor": p.NextCursor})
	case "data.get", "data.content":
		var b struct {
			ID     string `json:"id"`
			Offset int64  `json:"offset"`
			Limit  int    `json:"limit"`
		}
		if !decodeDataBody(w, r, &b, 4096) {
			return
		}
		o, e := owned(b.ID)
		if e != nil {
			writeDataError(w, e)
			return
		}
		if method == "data.get" {
			respond(publicDataObject(o))
			return
		}
		if b.Limit == 0 {
			b.Limit = data.ChunkBytes
		}
		chunk, e := store.Content(b.ID, a.scope(), b.Offset, b.Limit)
		if e != nil {
			writeDataError(w, e)
			return
		}
		respond(chunk)
	case "data.resolve":
		var b struct {
			Key string `json:"idempotencyKey"`
		}
		if !decodeDataBody(w, r, &b, 4096) {
			return
		}
		o, e := store.Resolve(b.Key, a.clientLabel)
		if e != nil {
			writeDataError(w, e)
			return
		}
		u, e := data.NewUploads(store).Resolve(b.Key, a.clientLabel, s.dataImportIdentity(a.clientLabel))
		if e != nil {
			writeDataError(w, e)
			return
		}
		var job *data.ImportStatus
		if s.dataImports != nil {
			job, e = s.dataImports.Resolve(b.Key, a.clientLabel)
			if e != nil {
				writeDataError(w, e)
				return
			}
		}
		var object any
		if o != nil {
			object = publicDataObject(*o)
		}
		respond(map[string]any{"object": object, "upload": u, "import": job})
	case "data.upload.begin":
		var b data.UploadRequest
		if !decodeDataBody(w, r, &b, 128<<10) {
			return
		}
		if b.Source == "" {
			b.Source = "relay"
		}
		st, e := data.NewUploads(store).Begin(b, a.clientLabel, s.dataImportIdentity(a.clientLabel))
		if e != nil {
			writeDataError(w, e)
			return
		}
		respond(st)
	default:
		var b struct {
			UploadID      string `json:"uploadId"`
			Offset        int64  `json:"offset"`
			ContentBase64 string `json:"contentBase64"`
		}
		if !decodeDataBody(w, r, &b, int64(base64.StdEncoding.EncodedLen(data.ChunkBytes)+(16<<10))) {
			return
		}
		u := data.NewUploads(store)
		identity := s.dataImportIdentity(a.clientLabel)
		var st data.UploadStatus
		var e error
		switch method {
		case "data.upload.status":
			st, e = u.Status(b.UploadID, a.clientLabel, identity)
		case "data.upload.commit":
			st, e = u.Commit(b.UploadID, a.clientLabel, identity)
		case "data.upload.abort":
			st, e = u.Abort(b.UploadID, a.clientLabel, identity)
		case "data.upload.chunk":
			if len(b.ContentBase64) > base64.StdEncoding.EncodedLen(data.ChunkBytes) {
				writeDataError(w, data.Error("TOO_LARGE", "chunk too large"))
				return
			}
			var raw []byte
			raw, e = base64.StdEncoding.Strict().DecodeString(b.ContentBase64)
			if e != nil {
				e = data.Error("INVALID_PARAMS", "invalid base64")
			} else {
				st, e = u.Chunk(b.UploadID, a.clientLabel, identity, b.Offset, raw)
			}
		}
		if e != nil {
			writeDataError(w, e)
			return
		}
		respond(st)
	}
}
