package daemon

import (
	"net/http"
	"strings"
	"time"

	"github.com/YoooClaw/cli/internal/version"
)

func (s *server) handleGatewayCompat(w http.ResponseWriter, r *http.Request, path string) {
	method := strings.TrimPrefix(path, "/gateway/")
	switch method {
	case "health":
		var body map[string]any
		if !decodeBody(w, r, &body) {
			return
		}
		payload := s.gatewayHealthPayload(body["echo"])
		gatewayOK(w, payload)
		// 与 hermes 一致：探针发现隧道拨的还是旧地址时先如实回 ok:false，
		// 再顺手把隧道重指到当前配置地址，App 稍后重试即可探到。
		if healthy, _ := payload["ok"].(bool); !healthy {
			s.retargetRelayIfStale()
		}
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

// gatewayHealthPayload 是 App 探活口。字段与 hermes-plugin 的 `req`/`health`
// 响应对齐（ok/time/sessionDb/lastInboundAt/relay.{stale,currentUrl,expectedUrl}/echo），
// 其余 yoooclaw 自有字段作为超集保留，老消费者不受影响。
func (s *server) gatewayHealthPayload(echo any) map[string]any {
	relayStatus := s.relayStatusPayload()
	stale, _ := relayStatus["stale"].(bool)
	s.st.mu.Lock()
	lastIngest := s.st.lastIngest
	ingestCount := s.st.ingestCount
	s.st.mu.Unlock()
	payload := map[string]any{
		"ok": !stale, "time": time.Now().UTC().Format(time.RFC3339), "sessionDb": false,
		"lastInboundAt": nilIfEmptyStr(lastIngest),
		"status":        "ok", "healthy": true, "server": "yoooclaw", "version": version.Version,
		"protocol": ProtocolVersion, "capabilities": Capabilities, "profile": s.ctx.Profile,
		"bind": s.bind, "port": s.port, "startedAt": s.st.startedAt,
		"lastIngestAt": nilIfEmptyStr(lastIngest), "ingestCount": ingestCount,
		"relay":    relayStatus,
		"features": map[string]any{"methods": gatewayMethodNames(), "capabilities": Capabilities},
	}
	if echo != nil {
		payload["echo"] = echo
	}
	return payload
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
