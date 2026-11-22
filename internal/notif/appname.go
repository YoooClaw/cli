package notif

import (
	_ "embed"
	"encoding/json"
	"os"
	"strings"
	"sync"
)

// appNameMapData 是随二进制分发的「包名 → 应用显示名」快照。
// 刷新方式见 docs/design/app-name-map-port.md；运行时只读，不访问网络。
//
//go:embed app-name-map.data.json
var appNameMapData []byte

// appNameMapFileEnv 允许测试/运维指定唯一快照来源；设置后不再回落到内置快照。
const appNameMapFileEnv = "YOOOCLAW_APP_NAME_MAP_FILE"

// localizedAppNames 是快照条目，任一语言可缺。
type localizedAppNames struct {
	Zh string `json:"zh,omitempty"`
	En string `json:"en,omitempty"`
}

var (
	appNamesOnce sync.Once
	appNames     map[string]localizedAppNames
)

// loadAppNames 解析内置或覆盖文件；任何失败都降级为空映射，绝不阻断通知落库。
func loadAppNames() {
	raw := appNameMapData
	if path := strings.TrimSpace(os.Getenv(appNameMapFileEnv)); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			appNames = map[string]localizedAppNames{}
			return
		}
		raw = data
	}
	appNames = parseAppNames(raw)
}

// parseAppNames 优先按当前格式（值为对象）解析；失败时兼容旧格式（值为字符串）和混合格式。
func parseAppNames(raw []byte) map[string]localizedAppNames {
	var current map[string]localizedAppNames
	if err := json.Unmarshal(raw, &current); err == nil {
		return normalizeAppNames(current)
	}

	var mixed map[string]json.RawMessage
	if err := json.Unmarshal(raw, &mixed); err != nil {
		return map[string]localizedAppNames{}
	}
	parsed := make(map[string]localizedAppNames, len(mixed))
	for pkg, value := range mixed {
		var names localizedAppNames
		if err := json.Unmarshal(value, &names); err == nil {
			parsed[pkg] = names
			continue
		}
		var legacy string
		if err := json.Unmarshal(value, &legacy); err == nil {
			parsed[pkg] = localizedAppNames{Zh: legacy}
		}
	}
	return normalizeAppNames(parsed)
}

// normalizeAppNames 去掉空白键、空名字，并统一裁剪名字两端空白。
func normalizeAppNames(in map[string]localizedAppNames) map[string]localizedAppNames {
	out := make(map[string]localizedAppNames, len(in))
	for pkg, names := range in {
		key := strings.TrimSpace(pkg)
		if key == "" {
			continue
		}
		names.Zh = strings.TrimSpace(names.Zh)
		names.En = strings.TrimSpace(names.En)
		if names.Zh == "" && names.En == "" {
			continue
		}
		out[key] = names
	}
	return out
}

// LookupAppDisplayName 返回中文优先的显示名（zh → en）。
// 命中返回 true；未收录、名字为空或包名为空返回 false。
func LookupAppDisplayName(packageName string) (string, bool) {
	key := strings.TrimSpace(packageName)
	if key == "" {
		return "", false
	}
	appNamesOnce.Do(loadAppNames)
	names, ok := appNames[key]
	if !ok {
		return "", false
	}
	if names.Zh != "" {
		return names.Zh, true
	}
	if names.En != "" {
		return names.En, true
	}
	return "", false
}

// ResolveAppDisplayName 命中返回显示名；未命中原样返回包名，保持现有行为。
func ResolveAppDisplayName(packageName string) string {
	if name, ok := LookupAppDisplayName(packageName); ok {
		return name
	}
	return packageName
}

// AppNameMapSize 返回已加载的快照条目数（未加载时触发加载）。
func AppNameMapSize() int {
	appNamesOnce.Do(loadAppNames)
	return len(appNames)
}

// ResetAppNameMapForTest 丢弃已加载快照；仅供测试使用。
// 不要在并发读取的用例中调用，相关用例不要 t.Parallel。
func ResetAppNameMapForTest() {
	appNamesOnce = sync.Once{}
	appNames = nil
}
