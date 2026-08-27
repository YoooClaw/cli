package daemon

import (
	"net/http"
	"strings"

	"github.com/YoooClaw/cli/internal/config"
	"github.com/YoooClaw/cli/internal/creds"
	"github.com/YoooClaw/cli/internal/lightgw"
)

// handleLightSend 处理 POST /light/send：核心逻辑在 lightgw.Send（与 CLI 共用）。
func (s *server) handleLightSend(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if !decodeBody(w, r, &body) {
		return
	}
	apiKey := creds.ResolveAPIKey().Value
	result, gerr := lightgw.Send(s.ctx.Paths.LightRules, body, config.ResolveCloudHost(s.cfg), apiKey, s.logger)
	if gerr != nil {
		status := gerr.Status
		if status == 0 {
			status = 400
		}
		writeJSON(w, status, errBody(gerr.Code, gerr.Message))
		return
	}
	writeJSON(w, 200, result)
}

// handleLightrules 处理 /gateway/lightrules.{list,create,update,delete}：
// 核心逻辑在 lightgw.Rules（与 CLI 共用），此处只做 HTTP 壳与日志。
func (s *server) handleLightrules(w http.ResponseWriter, r *http.Request, path string) {
	if r.Method != http.MethodPost {
		writeJSON(w, 404, errBody("YOOOCLAW_NOT_FOUND", "未知路径："+path))
		return
	}
	var body map[string]any
	if !decodeBody(w, r, &body) {
		return
	}
	method := strings.TrimPrefix(path, "/gateway/")
	payload, gerr := lightgw.Rules(s.ctx.Paths.LightRules, method, body)
	if gerr != nil {
		if gerr.Status == 404 {
			writeJSON(w, 404, errBody(gerr.Code, gerr.Message))
			return
		}
		gatewayErr(w, gerr.Code, gerr.Message)
		return
	}
	switch method {
	case "lightrules.create":
		s.logger.Info("Light rule created: " + asString(payload["name"]))
	case "lightrules.update":
		s.logger.Info("Light rule updated: " + asString(payload["name"]))
	case "lightrules.delete":
		s.logger.Info("Light rule deleted: " + asString(payload["name"]))
	}
	gatewayOK(w, payload)
}

// gatewayOK / gatewayErr 统一 gateway 响应外壳：CLI 读取 res.body.data。
func gatewayOK(w http.ResponseWriter, payload any) {
	writeJSON(w, 200, map[string]any{"ok": true, "data": payload})
}

func gatewayErr(w http.ResponseWriter, code, message string) {
	writeJSON(w, 200, map[string]any{"ok": false, "error": map[string]any{"code": code, "message": message}})
}

func gatewayErrData(w http.ResponseWriter, payload any) {
	writeJSON(w, 200, map[string]any{"ok": false, "error": payload})
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}
