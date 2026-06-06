package notif

import "strings"

// feishuFields 是从飞书通知派生的结构化字段。
type feishuFields struct {
	SenderName       string
	ConversationType string
	ConversationName string
}

type feishuNormalized struct {
	Title      string
	Content    string
	Structured feishuFields
}

func normOptText(v any) string {
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(s)
}

func isFeishuApp(appName string) bool {
	n := strings.ToLower(strings.TrimSpace(appName))
	if n == "" {
		return false
	}
	switch n {
	case "飞书", "feishu", "lark", "com.ss.android.lark", "com.bytedance.ee.lark", "com.larksuite.suite":
		return true
	}
	return strings.Contains(n, "feishu") || strings.Contains(n, "lark") || strings.Contains(n, "飞书")
}

func isFeishuAppLabel(text string) bool {
	t := strings.ToLower(strings.TrimSpace(text))
	return t == "飞书" || t == "lark" || t == "feishu"
}

func extractColonSender(body string) string {
	if body == "" {
		return ""
	}
	idx := -1
	for _, sep := range []string{":", "："} {
		if i := strings.Index(body, sep); i > 0 {
			if idx == -1 || i < idx {
				idx = i
			}
		}
	}
	if idx <= 0 {
		return ""
	}
	return strings.TrimSpace(body[:idx])
}

// findMetadataTextByKey 在嵌套 metadata 中递归查找 key（大小写不敏感），返回首个命中的归一文本。
func findMetadataTextByKey(value any, targetKey string, depth int) (found bool, text string) {
	if value == nil || depth > 4 {
		return false, ""
	}
	switch v := value.(type) {
	case []any:
		for _, item := range v {
			if f, t := findMetadataTextByKey(item, targetKey, depth+1); f {
				return true, t
			}
		}
		return false, ""
	case map[string]any:
		for key, child := range v {
			if strings.ToLower(strings.TrimSpace(key)) == targetKey {
				return true, normOptText(child)
			}
		}
		for _, child := range v {
			if f, t := findMetadataTextByKey(child, targetKey, depth+1); f {
				return true, t
			}
		}
		return false, ""
	default:
		return false, ""
	}
}

func deriveStructured(n RawNotification) feishuFields {
	found, subtitle := findMetadataTextByKey(n.Metadata, "subtitle", 0)
	if found {
		out := feishuFields{}
		if subtitle != "" {
			out.ConversationType = "group"
			out.ConversationName = subtitle
		} else {
			out.ConversationType = "private"
		}
		if sender := normOptText(n.Title); sender != "" {
			out.SenderName = sender
		}
		return out
	}
	if isFeishuAppLabel(n.Title) {
		if sender := extractColonSender(n.Body); sender != "" {
			return feishuFields{SenderName: sender, ConversationType: "private"}
		}
	}
	return feishuFields{}
}

func buildFeishuTitle(n RawNotification, s feishuFields) string {
	if s.ConversationType == "group" && s.ConversationName != "" {
		return s.ConversationName
	}
	if s.ConversationType == "private" && s.SenderName != "" {
		return s.SenderName
	}
	return n.Title
}

func buildFeishuContent(n RawNotification, s feishuFields) string {
	body := strings.TrimSpace(n.Body)
	if s.ConversationType == "group" && s.SenderName != "" && body != "" {
		if strings.HasPrefix(body, s.SenderName+":") || strings.HasPrefix(body, s.SenderName+"：") {
			return body
		}
		return s.SenderName + ": " + body
	}
	if s.ConversationType == "private" && s.SenderName != "" && body != "" {
		if strings.HasPrefix(body, s.SenderName+":") {
			return strings.TrimLeft(body[len(s.SenderName+":"):], " ")
		}
		if strings.HasPrefix(body, s.SenderName+"：") {
			return strings.TrimLeft(body[len(s.SenderName+"："):], " ")
		}
		return body
	}
	return body
}

// normalizeFeishuFields 归一化飞书通知；非飞书或无任何结构化命中返回 (nil,false)。
func normalizeFeishuFields(n RawNotification) (feishuNormalized, bool) {
	if !isFeishuApp(n.App) {
		return feishuNormalized{}, false
	}
	s := deriveStructured(n)
	if s.SenderName == "" && s.ConversationType == "" && s.ConversationName == "" {
		return feishuNormalized{}, false
	}
	return feishuNormalized{Title: buildFeishuTitle(n, s), Content: buildFeishuContent(n, s), Structured: s}, true
}
