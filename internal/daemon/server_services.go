package daemon

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/YoooClaw/cli/internal/monitor"
	"github.com/YoooClaw/cli/internal/notif"
)

// handleMonitors 处理 /monitors 与 /monitors/<name>[/enable|disable]。
func (s *server) handleMonitors(w http.ResponseWriter, r *http.Request, path string) {
	p := s.ctx.Paths
	switch {
	case path == "/monitors" && r.Method == http.MethodGet:
		writeJSON(w, 200, map[string]any{"ok": true, "monitors": monitor.List(p)})
	case path == "/monitors" && r.Method == http.MethodPost:
		var body struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			MatchRules  any    `json:"matchRules"`
			Schedule    string `json:"schedule"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		task, err := monitor.Create(p, monitor.CreateInput{
			Name: body.Name, Description: body.Description, MatchRules: body.MatchRules, Schedule: body.Schedule,
		})
		if err != nil {
			writeJSON(w, 400, errBody("INVALID_PARAMS", err.Error()))
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "monitor": task})
	case strings.HasPrefix(path, "/monitors/") && r.Method == http.MethodDelete:
		name := decodePathSeg(path[len("/monitors/"):])
		deleted, _ := monitor.Delete(p, name)
		writeJSON(w, 200, map[string]any{"ok": true, "deleted": deleted})
	case strings.HasPrefix(path, "/monitors/") && r.Method == http.MethodPost:
		// /monitors/<name>/<enable|disable>
		rest := path[len("/monitors/"):]
		idx := strings.LastIndex(rest, "/")
		if idx < 0 {
			writeJSON(w, 404, errBody("YOOOCLAW_NOT_FOUND", "未知路径："+path))
			return
		}
		name := decodePathSeg(rest[:idx])
		enabled := rest[idx+1:] == "enable"
		ok, _ := monitor.SetEnabled(p, name, enabled)
		writeJSON(w, 200, map[string]any{"ok": ok, "name": name, "enabled": enabled})
	default:
		writeJSON(w, 404, errBody("YOOOCLAW_NOT_FOUND", "未知路径："+path))
	}
}

// handleTunnel 处理 /tunnel/*。本 build 无 Relay 隧道（Phase 3），返回 standalone 语义。
func (s *server) handleTunnel(w http.ResponseWriter, r *http.Request, path string) {
	note := "Relay 隧道未实现（Phase 3）；当前仅直连 HTTP"
	switch path {
	case "/tunnel/status":
		writeJSON(w, 200, map[string]any{
			"ok": true, "mode": "standalone-http", "credentialMode": s.credentialSet.Mode,
			"connected": false, "relayUrl": s.cfg.Relay.URL, "enabled": s.cfg.Relay.Enabled,
			"note": note, "tunnels": []any{},
		})
	case "/tunnel/reconnect":
		writeJSON(w, 200, map[string]any{"ok": true, "mode": "standalone-http", "reconnected": false, "note": note})
	case "/tunnel/test":
		// 回环自检：直接本地 ingest 一条 echo 通知。
		_ = r
		res := s.storage.Ingest([]notif.RawNotification{{
			ID: "echo_local_" + time.Now().Format("20060102150405"), App: "yoooclaw.selftest",
			Title: "tunnel +test", Body: "echo", Timestamp: time.Now().Format(time.RFC3339),
		}}, "local")
		ok := res.Ingested > 0 || res.DedupedByID > 0 || res.DedupedByContent > 0
		writeJSON(w, 200, map[string]any{"ok": ok, "mode": "standalone-http", "loopback": map[string]any{"ok": ok}})
	default:
		writeJSON(w, 404, errBody("YOOOCLAW_NOT_FOUND", "未知路径："+path))
	}
}

func decodeBody(w http.ResponseWriter, r *http.Request, out any) bool {
	data, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, 400, errBody("INVALID_PARAMS", "读取请求体失败"))
		return false
	}
	if len(data) == 0 {
		return true
	}
	if json.Unmarshal(data, out) != nil {
		writeJSON(w, 400, errBody("INVALID_PARAMS", "请求体不是合法 JSON"))
		return false
	}
	return true
}

func decodePathSeg(seg string) string {
	if decoded, err := url.PathUnescape(seg); err == nil {
		return decoded
	}
	return seg
}
