package daemon

import (
	"errors"
	"net/http"
	"strings"

	"github.com/YoooClaw/cli/internal/webpage"
)

// handleWebPageIngest 接收浏览器扩展经 Relay 投递的网页正文（POST /web-pages）。
// 契约见 yc-web-extension/docs/page-context-design.md §4.1。
func (s *server) handleWebPageIngest(w http.ResponseWriter, r *http.Request, auth authResult) {
	var body webpage.Payload
	if !decodeBody(w, r, &body) {
		return
	}
	result, err := webpage.Ingest(s.ctx.Paths.WebPages, body, auth.clientLabel, s.logger)
	if err != nil {
		var ingestErr *webpage.Error
		if errors.As(err, &ingestErr) {
			writeJSON(w, ingestErr.Status, errBody(ingestErr.Code, ingestErr.Message))
			return
		}
		writeJSON(w, 500, errBody("WEB_PAGE_WRITE_FAILED", err.Error()))
		return
	}
	writeJSON(w, 200, result)
}

// handleWebPageStatus 回答「这些网页收过没有」（GET /web-pages/status?h=…&h=…），
// 供扩展 popup 打开时就地核对本地索引（§3.4）。
func (s *server) handleWebPageStatus(w http.ResponseWriter, r *http.Request) {
	hashes := r.URL.Query()["h"]
	writeJSON(w, 200, map[string]any{"saved": webpage.Status(s.ctx.Paths.WebPages, hashes)})
}

// handleWebPageIndex 返回索引（GET /web-pages/index?fields=hash,capturedAt），
// 供扩展登录后一次性回填本地收藏状态。
func (s *server) handleWebPageIndex(w http.ResponseWriter, r *http.Request) {
	entries := webpage.ReadIndex(s.ctx.Paths.WebPages)
	webpage.SortByCapturedDesc(entries)
	var fields []string
	if raw := strings.TrimSpace(r.URL.Query().Get("fields")); raw != "" {
		fields = strings.Split(raw, ",")
	}
	writeJSON(w, 200, map[string]any{"pages": webpage.ProjectFields(entries, fields)})
}
