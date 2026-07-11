package daemon

import (
	"net/http"
	"strings"

	"github.com/YoooClaw/cli/internal/creds"
	"github.com/YoooClaw/cli/internal/errs"
	"github.com/YoooClaw/cli/internal/light"
	"github.com/YoooClaw/cli/internal/lightrule"
)

// handleLightSend 处理 POST /light/send：segments / preset / rule 三选一。
// rule 从 Notification Intelligence 云端规则列表解析，不再读取本地 meta.json。
func (s *server) handleLightSend(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if !decodeBody(w, r, &body) {
		return
	}

	repeatInput := light.RepeatInputFromAny(body["repeat"], body["repeat_times"])
	reason, _ := body["reason"].(string)
	title, _ := body["title"].(string)
	bizUniqueID, _ := body["bizUniqueId"].(string)

	var segments []map[string]any
	switch {
	case body["segments"] != nil:
		res := light.ValidateSegments(body["segments"])
		if !res.Valid {
			writeJSON(w, 400, errBody("VALIDATION_FAILED", validationMessage(res)))
			return
		}
		segments = res.Segments
	case asString(body["preset"]) != "":
		preset, ok := light.LookupPreset(asString(body["preset"]))
		if !ok {
			writeJSON(w, 404, errBody("YOOOCLAW_NOT_FOUND", "未知预设："+asString(body["preset"])))
			return
		}
		segments = preset.Segments
		if body["repeat"] == nil && body["repeat_times"] == nil {
			rt := float64(preset.RepeatTimes)
			repeatInput = light.RepeatInput{RepeatTimes: &rt}
		}
	case asString(body["rule"]) != "":
		var err error
		segments, repeatInput, err = resolveCloudRuleEffect(asString(body["rule"]))
		if err != nil {
			writeCloudRuleHTTPError(w, err)
			return
		}
	default:
		writeJSON(w, 400, errBody("INVALID_PARAMS", "需要 segments / preset / rule 之一"))
		return
	}

	apiKey := creds.ResolveAPIKey().Value
	result := light.SendLightEffect(apiKey, segments, repeatInput, reason, title, s.logger, light.SendOptions{BizUniqueID: bizUniqueID})
	writeJSON(w, 200, result)
}

func resolveCloudRuleEffect(identifier string) ([]map[string]any, light.RepeatInput, error) {
	client, err := lightrule.NewCloudClient()
	if err != nil {
		return nil, light.RepeatInput{}, err
	}
	rules, err := client.List()
	if err != nil {
		return nil, light.RepeatInput{}, err
	}
	for _, item := range rules {
		rule, _ := item.(map[string]any)
		if rule == nil || (asString(rule["id"]) != identifier && asString(rule["name"]) != identifier) {
			continue
		}
		validation := light.ValidateSegments(rule["segments"])
		if !validation.Valid {
			return nil, light.RepeatInput{}, errs.New(errs.CodeInvalidArgument, validationMessage(validation))
		}
		return validation.Segments, light.RepeatInputFromAny(rule["repeat"], rule["repeat_times"]), nil
	}
	return nil, light.RepeatInput{}, errs.New(errs.CodeNotFound, "灯效规则不存在："+identifier)
}

// handleLightrules 保留 gateway 兼容协议，但所有 CRUD 都代理云端规则 API。
func (s *server) handleLightrules(w http.ResponseWriter, r *http.Request, path string) {
	if r.Method != http.MethodPost {
		writeJSON(w, 404, errBody("YOOOCLAW_NOT_FOUND", "未知路径："+path))
		return
	}
	client, err := lightrule.NewCloudClient()
	if err != nil {
		gatewayCloudRuleErr(w, err)
		return
	}

	method := strings.TrimPrefix(path, "/gateway/")
	switch method {
	case "lightrules.list":
		rules, err := client.List()
		if err != nil {
			gatewayCloudRuleErr(w, err)
			return
		}
		gatewayOK(w, map[string]any{"ok": true, "rules": rules})

	case "lightrules.create":
		var body map[string]any
		if !decodeBody(w, r, &body) {
			return
		}
		result, err := client.Create(asString(body["ruleText"]))
		if err != nil {
			gatewayCloudRuleErr(w, err)
			return
		}
		result["ok"] = true
		gatewayOK(w, result)

	case "lightrules.update":
		var body map[string]any
		if !decodeBody(w, r, &body) {
			return
		}
		identifier := resolveRuleIdentifier(body)
		for _, key := range []string{"id", "name", "ruleId", "ruleName"} {
			delete(body, key)
		}
		result, err := client.Update(identifier, body)
		if err != nil {
			gatewayCloudRuleErr(w, err)
			return
		}
		result["ok"] = true
		result["updated"] = true
		gatewayOK(w, result)

	case "lightrules.delete":
		var body map[string]any
		if !decodeBody(w, r, &body) {
			return
		}
		result, err := client.Delete(resolveRuleIdentifier(body))
		if err != nil {
			gatewayCloudRuleErr(w, err)
			return
		}
		result["ok"] = true
		result["deleted"] = true
		gatewayOK(w, result)

	default:
		writeJSON(w, 404, errBody("YOOOCLAW_NOT_FOUND", "未知路径："+path))
	}
}

func gatewayOK(w http.ResponseWriter, payload any) {
	writeJSON(w, 200, map[string]any{"ok": true, "data": payload})
}

func gatewayErr(w http.ResponseWriter, code, message string) {
	writeJSON(w, 200, map[string]any{"ok": false, "error": map[string]any{"code": code, "message": message}})
}

func gatewayCloudRuleErr(w http.ResponseWriter, err error) {
	if remote, ok := err.(*lightrule.RemoteError); ok {
		gatewayErr(w, remote.Code, remote.Message)
		return
	}
	if structured, ok := err.(*errs.Error); ok {
		gatewayErr(w, structured.Code, structured.Message)
		return
	}
	gatewayErr(w, "INTERNAL_ERROR", err.Error())
}

func writeCloudRuleHTTPError(w http.ResponseWriter, err error) {
	if remote, ok := err.(*lightrule.RemoteError); ok {
		status := remote.Status
		if status < 400 {
			status = http.StatusBadGateway
		}
		writeJSON(w, status, errBody(remote.Code, remote.Message))
		return
	}
	if structured, ok := err.(*errs.Error); ok {
		status := http.StatusBadRequest
		if structured.Code == errs.CodeCredentialMissing {
			status = http.StatusUnauthorized
		} else if structured.Code == errs.CodeNotFound {
			status = http.StatusNotFound
		}
		writeJSON(w, status, errBody(structured.Code, structured.Message))
		return
	}
	writeJSON(w, http.StatusBadGateway, errBody("INTERNAL_ERROR", err.Error()))
}

func resolveRuleIdentifier(body map[string]any) string {
	for _, key := range []string{"id", "name", "ruleId", "ruleName"} {
		if value := strings.TrimSpace(asString(body[key])); value != "" {
			return strings.TrimSuffix(value, ".json")
		}
	}
	return ""
}

func validationMessage(res light.ValidationResult) string {
	parts := make([]string, len(res.Errors))
	for i, item := range res.Errors {
		parts[i] = item.Field + ": " + item.Message
	}
	return strings.Join(parts, "; ")
}

func asString(value any) string {
	text, _ := value.(string)
	return text
}
