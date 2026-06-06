package config

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/YoooClaw/cli/internal/errs"
	"github.com/YoooClaw/cli/internal/fsutil"
	"github.com/YoooClaw/cli/internal/paths"
)

// LoadRaw 读取 config.json 为通用 map（用于 set/unset/show 的点号路径操作）。
// 文件不存在则返回默认 config 序列化后的 map。
func LoadRaw(p paths.Paths) (map[string]any, error) {
	var m map[string]any
	exists, err := fsutil.ReadJSON(p.Config, &m)
	if err != nil {
		return nil, err
	}
	if !exists {
		return toMap(Default(p.Credentials)), nil
	}
	return m, nil
}

// SaveRaw 把通用 map 写回 config.json。
func SaveRaw(p paths.Paths, m map[string]any) error {
	return fsutil.WriteJSON(p.Config, m, fsutil.ConfigFileMode)
}

// LoadMergedMap 加载「默认 + 存储」深合并后的完整 config，并转成 map（供 set/unset 写回全量）。
func LoadMergedMap(p paths.Paths) (map[string]any, error) {
	cfg, err := Load(p)
	if err != nil {
		return nil, err
	}
	return toMap(cfg), nil
}

// ToMap 把 Config 转成与磁盘一致的 map 形态。
func ToMap(cfg Config) map[string]any {
	return toMap(cfg)
}

func toMap(cfg Config) map[string]any {
	// 经 JSON 往返得到与磁盘一致的 map 形态。
	out := map[string]any{}
	b, _ := json.Marshal(cfg)
	_ = json.Unmarshal(b, &out)
	return out
}

// GetByPath 按点号路径取值。
func GetByPath(obj any, path string) (any, bool) {
	cursor := obj
	for _, seg := range strings.Split(path, ".") {
		m, ok := cursor.(map[string]any)
		if !ok {
			return nil, false
		}
		cursor, ok = m[seg]
		if !ok {
			return nil, false
		}
	}
	return cursor, true
}

// SetByPath 按点号路径设值（中间缺失对象自动创建）。
func SetByPath(obj map[string]any, path string, value any) {
	segs := strings.Split(path, ".")
	cursor := obj
	for i := 0; i < len(segs)-1; i++ {
		next, ok := cursor[segs[i]].(map[string]any)
		if !ok {
			next = map[string]any{}
			cursor[segs[i]] = next
		}
		cursor = next
	}
	cursor[segs[len(segs)-1]] = value
}

// UnsetByPath 按点号路径删除；返回是否删除了字段。
func UnsetByPath(obj map[string]any, path string) bool {
	segs := strings.Split(path, ".")
	cursor := obj
	for i := 0; i < len(segs)-1; i++ {
		next, ok := cursor[segs[i]].(map[string]any)
		if !ok {
			return false
		}
		cursor = next
	}
	last := segs[len(segs)-1]
	if _, ok := cursor[last]; !ok {
		return false
	}
	delete(cursor, last)
	return true
}

var numberPaths = map[string]bool{
	"daemon.port":                    true,
	"relay.heartbeatSec":             true,
	"relay.reconnectBackoffMs":       true,
	"notification.retentionDays":     true,
	"lightRules.evaluator.timeoutMs": true,
	"lightRules.evaluator.retries":   true,
	"image.maxBytes":                 true,
}

var booleanPaths = map[string]bool{
	"daemon.detach":      true,
	"relay.enabled":      true,
	"lightRules.enabled": true,
	"autoUpdate.enabled": true,
}

var arrayPaths = map[string]bool{
	"notification.ignoredApps": true,
}

// CoerceValue 把命令行字符串值按目标字段类型强转。
func CoerceValue(path, raw string) (any, error) {
	switch {
	case numberPaths[path]:
		if path == "notification.retentionDays" && (raw == "null" || raw == "") {
			return nil, nil
		}
		n, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return nil, errs.Newf(errs.CodeInvalidArgument, "%s 需要数字，收到：%s", path, raw)
		}
		return n, nil
	case booleanPaths[path]:
		switch raw {
		case "true":
			return true, nil
		case "false":
			return false, nil
		}
		return nil, errs.Newf(errs.CodeInvalidArgument, "%s 需要 true/false，收到：%s", path, raw)
	case arrayPaths[path]:
		parts := strings.Split(raw, ",")
		out := make([]any, 0, len(parts))
		for _, s := range parts {
			if t := strings.TrimSpace(s); t != "" {
				out = append(out, t)
			}
		}
		return out, nil
	default:
		return raw, nil
	}
}

// Mask 深拷贝并遮罩敏感字段，供 config show 用。inline: 引用整体遮罩。
func Mask(m map[string]any) map[string]any {
	clone := deepCloneMap(m)
	for _, path := range SecretRefPaths {
		if v, ok := GetByPath(clone, path); ok {
			if s, ok := v.(string); ok && strings.HasPrefix(s, "inline:") {
				SetByPath(clone, path, "inline:****")
			}
		}
	}
	return clone
}

func deepCloneMap(m map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		if sub, ok := v.(map[string]any); ok {
			out[k] = deepCloneMap(sub)
		} else {
			out[k] = v
		}
	}
	return out
}
