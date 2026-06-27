package daemon

import (
	"net/http"
	"strings"

	"github.com/YoooClaw/cli/internal/version"
)

func (s *server) handleGatewayCompat(w http.ResponseWriter, r *http.Request, path string) {
	method := strings.TrimPrefix(path, "/gateway/")
	switch method {
	case "health":
		gatewayOK(w, s.gatewayHealthPayload())
	case "channels.status":
		gatewayOK(w, s.gatewayChannelsPayload())
	case "agents.list":
		gatewayOK(w, map[string]any{"total": 0, "agents": []any{}, "items": []any{}})
	case "sessions.list":
		gatewayOK(w, map[string]any{"total": 0, "sessions": []any{}, "items": []any{}})
	case "chat.history":
		var body map[string]any
		if !decodeBody(w, r, &body) {
			return
		}
		sessionKey, _ := body["sessionKey"].(string)
		gatewayOK(w, map[string]any{"sessionKey": nilIfEmptyStr(sessionKey), "total": 0, "messages": []any{}})
	case "usage.cost":
		gatewayOK(w, map[string]any{"totalCost": 0, "cost": 0, "currency": "USD", "items": []any{}, "usage": []any{}})
	case "cron.list":
		gatewayOK(w, map[string]any{"total": 0, "crons": []any{}, "tasks": []any{}, "items": []any{}})
	case "wake":
		gatewayOK(w, map[string]any{"ok": true, "woken": true})
	default:
		gatewayErr(w, "METHOD_NOT_FOUND", "未注册的 gateway 方法："+method)
	}
}

func (s *server) gatewayHealthPayload() map[string]any {
	relayStatus := s.relayStatusPayload()
	s.st.mu.Lock()
	lastIngest := s.st.lastIngest
	ingestCount := s.st.ingestCount
	s.st.mu.Unlock()
	return map[string]any{
		"status": "ok", "healthy": true, "server": "yoooclaw", "version": version.Version,
		"protocol": ProtocolVersion, "capabilities": Capabilities, "profile": s.ctx.Profile,
		"bind": s.bind, "port": s.port, "startedAt": s.st.startedAt,
		"lastIngestAt": nilIfEmptyStr(lastIngest), "ingestCount": ingestCount,
		"relay":    relayStatus,
		"features": map[string]any{"methods": gatewayMethodNames(), "capabilities": Capabilities},
	}
}

func (s *server) gatewayChannelsPayload() map[string]any {
	relayStatus := s.relayStatusPayload()
	account := map[string]any{
		"id": "yoooclaw.daemon/" + s.ctx.Profile, "channel": "yoooclaw.daemon",
		"accountId": s.ctx.Profile, "label": "YoooClaw daemon (" + s.ctx.Profile + ")",
		"enabled": true, "running": true, "connected": relayStatus["connected"],
		"mode": relayStatus["mode"], "url": relayStatus["url"], "lastError": relayStatus["lastDisconnectReason"],
	}
	return map[string]any{
		"channelOrder": []string{"yoooclaw.daemon"},
		"channelMeta":  []map[string]any{{"id": "yoooclaw.daemon", "label": "YoooClaw daemon", "type": "daemon"}},
		"channels": []map[string]any{{
			"id": "yoooclaw.daemon", "label": "YoooClaw daemon", "enabled": true,
			"running": true, "connected": relayStatus["connected"], "accounts": []any{account},
		}},
		"accounts": []any{account},
		"relay":    relayStatus,
	}
}

func gatewayMethodNames() []string {
	return []string{
		"agents.list", "channels.status", "chat.history", "cron.list", "health",
		"lightrules.create", "lightrules.delete", "lightrules.list", "lightrules.update",
		"notifications.push", "recordings.asr.init", "recordings.delete", "recordings.list",
		"recordings.rename", "recordings.result.write", "recordings.retranscribe", "recordings.status",
		"sessions.list", "usage.cost", "wake",
	}
}
