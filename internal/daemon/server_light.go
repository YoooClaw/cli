package daemon

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/YoooClaw/cli/internal/creds"
	"github.com/YoooClaw/cli/internal/light"
	"github.com/YoooClaw/cli/internal/lightrule"
)

// handleLightSend 处理 POST /light/send：segments / preset / rule 三选一，编码后下发灯效云 API。
func (s *server) handleLightSend(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if !decodeBody(w, r, &body) {
		return
	}

	repeatInput := light.RepeatInputFromAny(body["repeat"], body["repeat_times"])
	reason, _ := body["reason"].(string)
	title, _ := body["title"].(string)

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
		rule := lightrule.Get(s.ctx.Paths.LightRules, asString(body["rule"]))
		if rule == nil {
			writeJSON(w, 404, errBody("YOOOCLAW_NOT_FOUND", "灯效规则不存在："+asString(body["rule"])))
			return
		}
		segments = rule.Segments
		if body["repeat"] == nil && body["repeat_times"] == nil {
			rt := float64(rule.RepeatTimes)
			repeatInput = light.RepeatInput{RepeatTimes: &rt}
		}
	default:
		writeJSON(w, 400, errBody("INVALID_PARAMS", "需要 segments / preset / rule 之一"))
		return
	}

	apiKey := creds.ResolveAPIKey().Value
	result := light.SendLightEffect(apiKey, segments, repeatInput, reason, title, s.logger)
	writeJSON(w, 200, result)
}

// handleLightrules 处理 /gateway/lightrules.{list,create,update,delete}。
func (s *server) handleLightrules(w http.ResponseWriter, r *http.Request, path string) {
	if r.Method != http.MethodPost {
		writeJSON(w, 404, errBody("YOOOCLAW_NOT_FOUND", "未知路径："+path))
		return
	}
	method := strings.TrimPrefix(path, "/gateway/")
	base := s.ctx.Paths.LightRules

	switch method {
	case "lightrules.list":
		rules := []map[string]any{}
		for _, m := range lightrule.List(base) {
			rm := metaToMap(m)
			rm["id"] = m.Name
			rules = append(rules, rm)
		}
		gatewayOK(w, map[string]any{"ok": true, "rules": rules})

	case "lightrules.create":
		var body map[string]any
		if !decodeBody(w, r, &body) {
			return
		}
		name := asString(body["name"])
		if name == "" {
			gatewayErr(w, "INVALID_PARAMS", "name is required")
			return
		}
		description := asString(body["description"])
		if description == "" {
			gatewayErr(w, "INVALID_PARAMS", "description is required")
			return
		}
		title := strings.TrimSpace(asString(body["title"]))
		if title == "" {
			title = name
		}
		res := light.ValidateSegments(body["segments"])
		if !res.Valid {
			gatewayErr(w, "VALIDATION_FAILED", validationErrorsJSON(res))
			return
		}
		meta, err := lightrule.Create(base, lightrule.CreateParams{
			Name: name, Title: title, Description: description, Segments: res.Segments,
			Repeat: boolPtrFromAny(body["repeat"]), RepeatTimes: floatPtrFromAny(body["repeat_times"]),
		})
		if err != nil {
			gatewayRuleErr(w, err)
			return
		}
		s.logger.Info("Light rule created: " + name)
		gatewayOK(w, map[string]any{"ok": true, "id": meta.Name, "name": meta.Name, "title": meta.Title, "rule": metaToMap(*meta)})

	case "lightrules.update":
		var body map[string]any
		if !decodeBody(w, r, &body) {
			return
		}
		name := resolveRuleIdentifier(body)
		if name == "" {
			gatewayErr(w, "INVALID_PARAMS", "name is required (or provide id/ruleId/ruleName)")
			return
		}
		params := lightrule.UpdateParams{
			Name:        name,
			Repeat:      boolPtrFromAny(body["repeat"]),
			RepeatTimes: floatPtrFromAny(body["repeat_times"]),
			Enabled:     boolPtrFromAny(body["enabled"]),
		}
		if raw, ok := body["title"]; ok {
			t := strings.TrimSpace(asString(raw))
			if t == "" {
				gatewayErr(w, "INVALID_PARAMS", "title must be a non-empty string")
				return
			}
			params.Title = &t
		}
		if raw, ok := body["description"]; ok {
			d, isStr := raw.(string)
			if !isStr {
				gatewayErr(w, "INVALID_PARAMS", "description must be a string")
				return
			}
			params.Description = &d
		}
		if body["segments"] != nil {
			res := light.ValidateSegments(body["segments"])
			if !res.Valid {
				gatewayErr(w, "VALIDATION_FAILED", validationErrorsJSON(res))
				return
			}
			params.Segments = res.Segments
			params.HasSegments = true
		}
		meta, err := lightrule.Update(base, params)
		if err != nil {
			gatewayRuleErr(w, err)
			return
		}
		s.logger.Info("Light rule updated: " + name)
		gatewayOK(w, map[string]any{"ok": true, "id": meta.Name, "name": meta.Name, "title": meta.Title, "updated": true, "rule": metaToMap(*meta)})

	case "lightrules.delete":
		var body map[string]any
		if !decodeBody(w, r, &body) {
			return
		}
		name := resolveRuleIdentifier(body)
		if name == "" {
			gatewayErr(w, "INVALID_PARAMS", "name is required (or provide id/ruleId/ruleName)")
			return
		}
		deleted, err := lightrule.Delete(base, name)
		if err != nil {
			gatewayRuleErr(w, err)
			return
		}
		s.logger.Info("Light rule deleted: " + deleted)
		gatewayOK(w, map[string]any{"ok": true, "id": deleted, "name": deleted, "deleted": true})

	default:
		writeJSON(w, 404, errBody("YOOOCLAW_NOT_FOUND", "未知路径："+path))
	}
}

// gatewayOK / gatewayErr 统一 gateway 响应外壳：CLI 读取 res.body.data。
func gatewayOK(w http.ResponseWriter, payload any) {
	writeJSON(w, 200, map[string]any{"ok": true, "data": payload})
}

func gatewayErr(w http.ResponseWriter, code, message string) {
	writeJSON(w, 200, map[string]any{"ok": false, "error": map[string]any{"code": code, "message": message}})
}

func gatewayRuleErr(w http.ResponseWriter, err error) {
	if e, ok := err.(*lightrule.Error); ok {
		gatewayErr(w, e.Code, e.Message)
		return
	}
	gatewayErr(w, "INTERNAL_ERROR", err.Error())
}

func resolveRuleIdentifier(body map[string]any) string {
	for _, key := range []string{"name", "id", "ruleId", "ruleName"} {
		if s := asString(body[key]); s != "" {
			n := strings.TrimSpace(s)
			if strings.HasSuffix(strings.ToLower(n), ".json") {
				n = n[:len(n)-len(".json")]
			}
			if n != "" {
				return n
			}
		}
	}
	return ""
}

func metaToMap(m lightrule.Meta) map[string]any {
	data, _ := json.Marshal(m)
	var out map[string]any
	_ = json.Unmarshal(data, &out)
	return out
}

func validationMessage(res light.ValidationResult) string {
	parts := make([]string, len(res.Errors))
	for i, e := range res.Errors {
		parts[i] = e.Field + ": " + e.Message
	}
	return strings.Join(parts, "; ")
}

func validationErrorsJSON(res light.ValidationResult) string {
	data, _ := json.Marshal(res.Errors)
	return string(data)
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

func boolPtrFromAny(v any) *bool {
	if b, ok := v.(bool); ok {
		return &b
	}
	return nil
}

func floatPtrFromAny(v any) *float64 {
	if f, ok := v.(float64); ok {
		return &f
	}
	return nil
}
