// Package lightgw 承载灯效 gateway 的核心逻辑（lightrules CRUD + light send），
// 供 daemon HTTP handler 与 CLI 命令共用——CLI 侧从此不依赖 daemon 进程。
// （light 与 lightrule 互不引用，本包同时引两者，避免循环依赖。）
package lightgw

import (
	"encoding/json"
	"strings"

	"github.com/YoooClaw/cli/internal/light"
	"github.com/YoooClaw/cli/internal/lightrule"
)

// Err 是 gateway 语义的结构化错误；Status 供非 gateway 壳的 HTTP 路径
// （/light/send）区分 400/404，gateway 壳路径恒为 200 + ok:false。
type Err struct {
	Code    string
	Message string
	Status  int
}

func (e *Err) Error() string { return e.Message }

// Rules 处理 lightrules.{list,create,update,delete}（原 daemon handleLightrules 核心）。
// 返回 gatewayOK 的 payload；未知 method 返回 code=YOOOCLAW_NOT_FOUND、Status=404。
func Rules(base, method string, body map[string]any) (map[string]any, *Err) {
	switch method {
	case "lightrules.list":
		rules := []map[string]any{}
		for _, m := range lightrule.List(base) {
			rm := metaToMap(m)
			rm["id"] = m.Name
			rules = append(rules, rm)
		}
		return map[string]any{"ok": true, "rules": rules}, nil

	case "lightrules.create":
		name := asString(body["name"])
		if name == "" {
			return nil, &Err{Code: "INVALID_PARAMS", Message: "name is required"}
		}
		description := asString(body["description"])
		if description == "" {
			return nil, &Err{Code: "INVALID_PARAMS", Message: "description is required"}
		}
		title := strings.TrimSpace(asString(body["title"]))
		if title == "" {
			title = name
		}
		res := light.ValidateSegments(body["segments"])
		if !res.Valid {
			return nil, &Err{Code: "VALIDATION_FAILED", Message: validationErrorsJSON(res)}
		}
		meta, err := lightrule.Create(base, lightrule.CreateParams{
			Name: name, Title: title, Description: description, Segments: res.Segments,
			Repeat: boolPtrFromAny(body["repeat"]), RepeatTimes: floatPtrFromAny(body["repeat_times"]),
		})
		if err != nil {
			return nil, ruleErr(err)
		}
		return map[string]any{"ok": true, "id": meta.Name, "name": meta.Name, "title": meta.Title, "rule": metaToMap(*meta)}, nil

	case "lightrules.update":
		name := ResolveRuleIdentifier(body)
		if name == "" {
			return nil, &Err{Code: "INVALID_PARAMS", Message: "name is required (or provide id/ruleId/ruleName)"}
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
				return nil, &Err{Code: "INVALID_PARAMS", Message: "title must be a non-empty string"}
			}
			params.Title = &t
		}
		if raw, ok := body["description"]; ok {
			d, isStr := raw.(string)
			if !isStr {
				return nil, &Err{Code: "INVALID_PARAMS", Message: "description must be a string"}
			}
			params.Description = &d
		}
		if body["segments"] != nil {
			res := light.ValidateSegments(body["segments"])
			if !res.Valid {
				return nil, &Err{Code: "VALIDATION_FAILED", Message: validationErrorsJSON(res)}
			}
			params.Segments = res.Segments
			params.HasSegments = true
		}
		meta, err := lightrule.Update(base, params)
		if err != nil {
			return nil, ruleErr(err)
		}
		return map[string]any{"ok": true, "id": meta.Name, "name": meta.Name, "title": meta.Title, "updated": true, "rule": metaToMap(*meta)}, nil

	case "lightrules.delete":
		name := ResolveRuleIdentifier(body)
		if name == "" {
			return nil, &Err{Code: "INVALID_PARAMS", Message: "name is required (or provide id/ruleId/ruleName)"}
		}
		deleted, err := lightrule.Delete(base, name)
		if err != nil {
			return nil, ruleErr(err)
		}
		return map[string]any{"ok": true, "id": deleted, "name": deleted, "deleted": true}, nil

	default:
		return nil, &Err{Code: "YOOOCLAW_NOT_FOUND", Message: "未知路径：/gateway/" + method, Status: 404}
	}
}

// Send 处理 /light/send 核心（原 daemon handleLightSend）：segments / preset /
// rule 三选一，编码后下发灯效云 API。host 见 light.LightAPIURL。
func Send(base string, body map[string]any, host, apiKey string, logger light.Logger) (any, *Err) {
	repeatInput := light.RepeatInputFromAny(body["repeat"], body["repeat_times"])
	reason, _ := body["reason"].(string)
	title, _ := body["title"].(string)

	var segments []map[string]any
	switch {
	case body["segments"] != nil:
		res := light.ValidateSegments(body["segments"])
		if !res.Valid {
			return nil, &Err{Code: "VALIDATION_FAILED", Message: validationMessage(res), Status: 400}
		}
		segments = res.Segments
	case asString(body["preset"]) != "":
		preset, ok := light.LookupPreset(asString(body["preset"]))
		if !ok {
			return nil, &Err{Code: "YOOOCLAW_NOT_FOUND", Message: "未知预设：" + asString(body["preset"]), Status: 404}
		}
		segments = preset.Segments
		if body["repeat"] == nil && body["repeat_times"] == nil {
			rt := float64(preset.RepeatTimes)
			repeatInput = light.RepeatInput{RepeatTimes: &rt}
		}
	case asString(body["rule"]) != "":
		rule := lightrule.Get(base, asString(body["rule"]))
		if rule == nil {
			return nil, &Err{Code: "YOOOCLAW_NOT_FOUND", Message: "灯效规则不存在：" + asString(body["rule"]), Status: 404}
		}
		segments = rule.Segments
		if body["repeat"] == nil && body["repeat_times"] == nil {
			rt := float64(rule.RepeatTimes)
			repeatInput = light.RepeatInput{RepeatTimes: &rt}
		}
	default:
		return nil, &Err{Code: "INVALID_PARAMS", Message: "需要 segments / preset / rule 之一", Status: 400}
	}

	return light.SendLightEffect(host, apiKey, segments, repeatInput, reason, title, logger), nil
}

func ruleErr(err error) *Err {
	if e, ok := err.(*lightrule.Error); ok {
		return &Err{Code: e.Code, Message: e.Message}
	}
	return &Err{Code: "INTERNAL_ERROR", Message: err.Error()}
}

// ResolveRuleIdentifier 从 body 的 name/id/ruleId/ruleName 解析规则名（剥 .json 后缀）。
func ResolveRuleIdentifier(body map[string]any) string {
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
