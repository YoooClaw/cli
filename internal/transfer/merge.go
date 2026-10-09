package transfer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/YoooClaw/cli/internal/image"
	"github.com/YoooClaw/cli/internal/notif"
	"github.com/YoooClaw/cli/internal/recording"
	"github.com/YoooClaw/cli/internal/webpage"
)

// 合并结果（与插件 MergeResult 一致）。
const (
	OutcomeAdded        = "added"
	OutcomeSkipped      = "skipped"
	OutcomeConflict     = "conflict"
	OutcomeSupplemented = "supplemented"
	OutcomeFailed       = "failed"
)

// roundTrip 让数据过一遍本端的存储结构体。CLI 的存储是强类型的：结构体里
// 没有的字段、omitempty 的空值落盘后都会消失。比较前两边都先过一遍，
// 「同一条记录再导一次」才会稳定得到 skipped，而不是被存储层的有损序列化误判成 conflict。
func roundTrip(typ string, m map[string]any) (map[string]any, error) {
	var target any
	switch typ {
	case TypeNotifications:
		target = &notif.StoredNotification{}
	case TypeRecordings:
		target = &recording.Entry{}
	case TypeImages:
		target = &image.Entry{}
	case TypeWebPages:
		target = &webpage.Entry{}
	default:
		return nil, fail("INVALID_SCOPE")
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return nil, failf("INVALID_RECORD_DATA", "%s", err.Error())
	}
	out, _ := toAny(target).(map[string]any)
	return out, nil
}

func normalized(typ string, m map[string]any) (map[string]any, error) {
	rt, err := roundTrip(typ, m)
	if err != nil {
		return nil, err
	}
	return portable(typ, rt), nil
}

// matchKey 是目标端查找同一条记录用的键。通知的身份是内容哈希，要在
// 规范化之后计算；其余类型就是主键。
func matchKey(typ string, data map[string]any) string {
	if typ == TypeNotifications {
		if n, err := normalized(typ, data); err == nil {
			return identity(typ, n)
		}
	}
	return identity(typ, portable(typ, data))
}

// compare 判定一条迁入记录相对目标端当前条目的合并结果。
func compare(r Record, current map[string]any, root string) (string, error) {
	if current == nil {
		return OutcomeAdded, nil
	}
	a, err := normalized(r.Type, current)
	if err != nil {
		return "", err
	}
	b, err := normalized(r.Type, r.Data)
	if err != nil {
		return "", err
	}
	if canonical(a) != canonical(b) {
		return OutcomeConflict, nil
	}
	missing := false
	for _, asset := range r.Assets {
		rel, _ := current[asset.Field].(string)
		if rel == "" || !pathExists(filepath.Join(root, rel)) {
			missing = true
			continue
		}
		p, err := safeFile(root, rel)
		if err != nil {
			return OutcomeConflict, nil
		}
		if h, err := hashFile(p); err != nil || h != asset.Hash {
			return OutcomeConflict, nil
		}
	}
	if missing {
		return OutcomeSupplemented, nil
	}
	return OutcomeSkipped, nil
}

// materialize 生成要写入目标索引的条目：附件按内容寻址落到 files/，
// 不覆盖任何无关文件。必须在存储的临界区内调用。
func materialize(r Record, root, packageRoot string, current map[string]any) (map[string]any, error) {
	base := current
	if base == nil {
		base = r.Data
	}
	entry := make(map[string]any, len(base)+2)
	for k, v := range base {
		entry[k] = v
	}
	// 只有真正新增的记录才打迁移标记；补附件时不能把本地产生的记录标成迁入的。
	if current == nil {
		entry["transfer"] = map[string]any{"origin": r.Origin, "recordId": r.RecordID}
	}
	assetBytes := map[string]int64{}
	for _, a := range r.Assets {
		assetBytes[a.Field] = a.Bytes
		if rel, _ := current[a.Field].(string); rel != "" && pathExists(filepath.Join(root, rel)) {
			continue
		}
		name := "files/transfer-" + sha(r.Type + ":" + r.RecordID + ":" + a.Field)[:16] + "-" + a.Hash + a.Extension
		filesDir := filepath.Join(root, "files")
		if err := os.MkdirAll(filesDir, 0o700); err != nil {
			return nil, err
		}
		if info, err := os.Lstat(filesDir); err != nil || info.Mode()&os.ModeSymlink != 0 {
			return nil, fail("UNSAFE_TARGET_PATH")
		}
		dest := filepath.Join(root, name)
		if _, err := os.Lstat(dest); err == nil {
			p, err := safeFile(root, name)
			if err != nil {
				return nil, err
			}
			if h, err := hashFile(p); err != nil || h != a.Hash {
				return nil, fail("DESTINATION_CHANGED")
			}
		} else {
			src, err := safeFile(packageRoot, "blobs/"+a.Hash)
			if err != nil {
				return nil, err
			}
			if err := copyFile(src, dest); err != nil {
				_ = os.Remove(dest)
				return nil, err
			}
		}
		entry[a.Field] = name
	}
	switch r.Type {
	case TypeRecordings:
		if truthy(entry["transcriptFile"]) || truthy(entry["summaryFile"]) || truthy(entry["transcriptDataFile"]) {
			entry["status"] = recording.StatusTranscribed
		} else {
			entry["status"] = recording.StatusSynced
		}
		if truthy(entry["audioFile"]) {
			entry["audioStatus"] = recording.AudioStatusDownloaded
		} else {
			delete(entry, "audioStatus")
		}
		if !truthy(entry["ingestedAt"]) {
			entry["ingestedAt"] = time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
		}
		if !truthy(entry["updatedAt"]) {
			entry["updatedAt"] = entry["ingestedAt"]
		}
	case TypeImages:
		if truthy(entry["localFile"]) {
			entry["status"] = "synced"
		} else {
			entry["status"] = "sync_failed"
		}
	case TypeWebPages:
		if n, ok := assetBytes["relativePath"]; ok {
			entry["bytes"] = n
		}
		if truthy(entry["archivePath"]) {
			if n, ok := assetBytes["archivePath"]; ok {
				entry["archiveBytes"] = n
			}
		}
	}
	return entry, nil
}

func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case string:
		return x != ""
	case bool:
		return x
	case float64:
		return x != 0
	}
	return true
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func fromMap[T any](m map[string]any) (*T, error) {
	raw, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, failf("INVALID_RECORD_DATA", "%s", err.Error())
	}
	return &out, nil
}

func toMap(v any) map[string]any {
	m, _ := toAny(v).(map[string]any)
	return m
}

// mergeEntry 是录音/图片/网页共用的「比较 → 需要时物化」流程，在各存储的锁内执行。
func mergeEntry[T any](r Record, root, packageRoot string, current *T, outcome *string) (*T, error) {
	var cur map[string]any
	if current != nil {
		cur = toMap(current)
	}
	result, err := compare(r, cur, root)
	if err != nil {
		return nil, err
	}
	*outcome = result
	if result != OutcomeAdded && result != OutcomeSupplemented {
		return nil, nil
	}
	entry, err := materialize(r, root, packageRoot, cur)
	if err != nil {
		return nil, err
	}
	return fromMap[T](entry)
}
