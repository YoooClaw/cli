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
	"github.com/YoooClaw/cli/internal/relay"
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

// handleTunnel 处理 /tunnel/*。
func (s *server) handleTunnel(w http.ResponseWriter, r *http.Request, path string) {
	switch path {
	case "/tunnel/status":
		relayStatus := s.relayStatusPayload()
		client := strings.TrimSpace(r.URL.Query().Get("client"))
		if client != "" && client != "all" {
			relayStatus["tunnels"] = filterRelayTunnels(relayStatus["tunnels"], client)
		}
		writeJSON(w, 200, map[string]any{
			"ok": true, "mode": relayStatus["mode"], "credentialMode": s.credentialSet.Mode,
			"defaultLabel": defaultEntryLabel(s.credentialSet),
			"connected":    relayStatus["connected"], "relayUrl": relayStatus["url"], "enabled": s.cfg.Relay.Enabled,
			"reconnectAttempt": relayStatus["reconnectAttempt"], "lastDisconnectReason": relayStatus["lastDisconnectReason"],
			"note": relayStatus["note"], "tunnels": relayStatus["tunnels"],
		})
	case "/tunnel/reconnect":
		if s.tunnelSupervisor == nil {
			relayStatus := s.relayStatusPayload()
			writeJSON(w, 200, map[string]any{"ok": true, "mode": relayStatus["mode"], "reconnected": false, "note": relayStatus["note"]})
			return
		}
		var body struct {
			Client string `json:"client"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		client := strings.TrimSpace(body.Client)
		if client == "all" {
			client = ""
		}
		reconnected, missing := s.tunnelSupervisor.Reconnect(client)
		if missing != "" {
			writeJSON(w, 404, errBody("YOOOCLAW_TUNNEL_LABEL_NOT_FOUND", "隧道不存在："+missing))
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "mode": "relay", "reconnected": true, "labels": reconnected})
	case "/tunnel/test":
		var body struct {
			Client string `json:"client"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		clientLabel := strings.TrimSpace(body.Client)
		if clientLabel == "" || clientLabel == "all" {
			clientLabel = "local"
		} else if !s.hasCredentialLabel(clientLabel) {
			writeJSON(w, 404, errBody("YOOOCLAW_TUNNEL_LABEL_NOT_FOUND", "隧道不存在："+clientLabel))
			return
		}
		res := s.storage.Ingest([]notif.RawNotification{{
			ID: "echo_local_" + time.Now().Format("20060102150405"), App: "yoooclaw.selftest",
			Title: "tunnel +test", Body: "echo", Timestamp: time.Now().Format(time.RFC3339),
		}}, clientLabel)
		s.recordIngest(res)
		ok := res.Ingested > 0 || res.DedupedByID > 0 || res.DedupedByContent > 0
		writeJSON(w, 200, map[string]any{"ok": ok, "mode": s.relayStatusPayload()["mode"], "clientLabel": clientLabel, "loopback": map[string]any{"ok": ok}})
	default:
		writeJSON(w, 404, errBody("YOOOCLAW_NOT_FOUND", "未知路径："+path))
	}
}

func (s *server) hasCredentialLabel(label string) bool {
	for _, entry := range s.credentialSet.Entries {
		if entry.Label == label {
			return true
		}
	}
	return false
}

func filterRelayTunnels(value any, label string) any {
	tunnels, ok := value.([]relay.TunnelStatus)
	if !ok {
		return value
	}
	out := make([]relay.TunnelStatus, 0, len(tunnels))
	for _, tunnel := range tunnels {
		if tunnel.Label == label {
			out = append(out, tunnel)
		}
	}
	return out
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
