package notif

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
)

// 日存储文件格式：
//   - 当前格式 YYYY-MM-DD.jsonl：每行一条 StoredNotification（compact JSON），只追加写。
//     逐行解析、坏行跳过，torn write 最多损失最后一行。
//   - 旧格式 YYYY-MM-DD.json：整天一个 JSON 数组，只读兼容（migrate 命令会原样拷入旧文件）；
//     写路径首次落盘该天时迁移成 .jsonl。
//   - 两者并存时以 .jsonl 为准：迁移先原子写 .jsonl 再删 .json，中途 crash 只会留下
//     一份内容已被 .jsonl 完整包含的过期副本。
var dateFileRE = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})\.jsonl?$`)

func dayFilePath(dir, dateKey string) string {
	return filepath.Join(dir, dateKey+".jsonl")
}

func legacyDayFilePath(dir, dateKey string) string {
	return filepath.Join(dir, dateKey+".json")
}

// listDateKeys 列出 YYYY-MM-DD 日期 key（新旧格式合并去重），降序。
func listDateKeys(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var keys []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if m := dateFileRE.FindStringSubmatch(e.Name()); m != nil && !seen[m[1]] {
			seen[m[1]] = true
			keys = append(keys, m[1])
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(keys)))
	return keys
}

// encodeJSONLines 把条目序列化为 JSONL（每行一条 + 换行）。
func encodeJSONLines(items []StoredNotification) ([]byte, error) {
	var buf bytes.Buffer
	for _, item := range items {
		b, err := json.Marshal(item)
		if err != nil {
			return nil, err
		}
		buf.Write(b)
		buf.WriteByte('\n')
	}
	return buf.Bytes(), nil
}

// decodeJSONLines 逐行解析 JSONL，坏行跳过并计数（torn write 的半行落在这里）。
func decodeJSONLines(raw []byte) (items []StoredNotification, skipped int) {
	for _, line := range bytes.Split(raw, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var item StoredNotification
		if err := json.Unmarshal(line, &item); err != nil {
			skipped++
			continue
		}
		items = append(items, item)
	}
	return items, skipped
}

// readDateFile 读取某天的条目（只读路径用，容错：读不出来当空处理）。
// .jsonl 存在时以它为准，否则回退旧 .json 数组。
func readDateFile(dir, dateKey string) []StoredNotification {
	if raw, err := os.ReadFile(dayFilePath(dir, dateKey)); err == nil {
		items, _ := decodeJSONLines(raw)
		return items
	}
	raw, err := os.ReadFile(legacyDayFilePath(dir, dateKey))
	if err != nil {
		return nil
	}
	var items []StoredNotification
	if json.Unmarshal(raw, &items) != nil {
		return nil
	}
	return items
}
