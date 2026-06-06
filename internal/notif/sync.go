package notif

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"

	"github.com/YoooClaw/cli/internal/errs"
	"github.com/YoooClaw/cli/internal/fsutil"
)

// SyncFetchLimit 单批 fetch 返回上限（对齐 TS SYNC_FETCH_LIMIT）。
const SyncFetchLimit = 300

// checkpointEntry 是每个日期的消费进度（已处理到的最大下标）。
type checkpointEntry struct {
	LastIndex int `json:"lastIndex"`
}

func checkpointPath(dir string) string {
	return filepath.Join(dir, ".checkpoint.json")
}

// readCheckpoint 读取 checkpoint；不存在 / 损坏时回退为空表（对齐 TS 容错语义）。
func readCheckpoint(dir string) map[string]checkpointEntry {
	out := map[string]checkpointEntry{}
	raw, err := os.ReadFile(checkpointPath(dir))
	if err != nil {
		return out
	}
	if json.Unmarshal(raw, &out) != nil {
		return map[string]checkpointEntry{}
	}
	return out
}

func writeCheckpoint(dir string, data map[string]checkpointEntry) error {
	return fsutil.WriteJSON(checkpointPath(dir), data, fsutil.ConfigFileMode)
}

// lastIndexFor 返回某日期的 checkpoint 进度，无记录返回 -1。
func lastIndexFor(checkpoint map[string]checkpointEntry, dateKey string) int {
	if e, ok := checkpoint[dateKey]; ok {
		return e.LastIndex
	}
	return -1
}

func parseIndexOption(raw, label string) (int, error) {
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 || strconv.Itoa(n) != raw {
		return 0, errs.Newf(errs.CodeInvalidArgument, "%s 必须是非负整数", label)
	}
	return n, nil
}

// SyncPending 是 scan 返回的单个日期待同步摘要。
type SyncPending struct {
	Date       string `json:"date"`
	Count      int    `json:"count"`
	StartIndex int    `json:"startIndex"`
}

// ScanSync 扫描各日期未处理通知，返回待同步摘要。
func ScanSync(dir string) map[string]any {
	checkpoint := readCheckpoint(dir)
	pending := []SyncPending{}
	totalPending := 0
	for _, dateKey := range listDateKeys(dir) {
		items := readDateFile(dir, dateKey)
		lastIndex := lastIndexFor(checkpoint, dateKey)
		unprocessed := len(items) - (lastIndex + 1)
		if unprocessed > 0 {
			pending = append(pending, SyncPending{Date: dateKey, Count: unprocessed, StartIndex: lastIndex + 1})
			totalPending += unprocessed
		}
	}
	return map[string]any{"ok": true, "pending": pending, "totalPending": totalPending}
}

// FetchSync 返回指定日期未处理通知的一批快照（不推进 checkpoint）。
func FetchSync(dir, date, maxEndIndexRaw string) (map[string]any, error) {
	items := readDateFile(dir, date)
	if len(items) == 0 {
		return nil, errs.Newf(errs.CodeNotFound, "日期 %s 无通知数据", date)
	}
	checkpoint := readCheckpoint(dir)
	lastIndex := lastIndexFor(checkpoint, date)
	startIndex := lastIndex + 1

	maxEndIndex := len(items) - 1
	if maxEndIndexRaw != "" {
		n, err := parseIndexOption(maxEndIndexRaw, "--max-end-index")
		if err != nil {
			return nil, err
		}
		if n < maxEndIndex {
			maxEndIndex = n
		}
	}

	snapshotEndExclusive := startIndex
	if maxEndIndex+1 > snapshotEndExclusive {
		snapshotEndExclusive = maxEndIndex + 1
	}
	unprocessed := safeSlice(items, startIndex, snapshotEndExclusive)
	notifications := unprocessed
	if len(notifications) > SyncFetchLimit {
		notifications = notifications[:SyncFetchLimit]
	}

	endIndex := lastIndex
	if len(notifications) > 0 {
		endIndex = startIndex + len(notifications) - 1
	}
	hasMore := len(unprocessed) > len(notifications)
	var nextStartIndex any
	if hasMore {
		nextStartIndex = endIndex + 1
	}
	return map[string]any{
		"ok":               true,
		"date":             date,
		"startIndex":       startIndex,
		"endIndex":         endIndex,
		"nextStartIndex":   nextStartIndex,
		"limit":            SyncFetchLimit,
		"maxEndIndex":      maxEndIndex,
		"returned":         len(notifications),
		"totalUnprocessed": len(unprocessed),
		"hasMore":          hasMore,
		"notifications":    notifications,
	}, nil
}

// CommitSync 推进指定日期的 checkpoint 到 endIndex（缺省按单批上限）。
func CommitSync(dir, date, endIndexRaw string) (map[string]any, error) {
	items := readDateFile(dir, date)
	if len(items) == 0 {
		return nil, errs.Newf(errs.CodeNotFound, "日期 %s 无通知数据", date)
	}
	checkpoint := readCheckpoint(dir)
	lastIndex := lastIndexFor(checkpoint, date)

	var committedIndex int
	var commitMode string
	if endIndexRaw != "" {
		n, err := parseIndexOption(endIndexRaw, "--end-index")
		if err != nil {
			return nil, err
		}
		committedIndex = n
		if committedIndex >= len(items) {
			return nil, errs.New(errs.CodeInvalidArgument, "--end-index 超出当前日期通知文件范围")
		}
		if committedIndex < lastIndex {
			return nil, errs.New(errs.CodeInvalidArgument, "--end-index 早于当前 checkpoint，不能回退消费进度")
		}
		if committedIndex > lastIndex+SyncFetchLimit {
			return nil, errs.New(errs.CodeInvalidArgument, "--end-index 超出单批 fetch 上限，不能跳过未处理通知")
		}
		commitMode = "exact-end-index"
	} else {
		committedIndex = lastIndex + SyncFetchLimit
		if committedIndex > len(items)-1 {
			committedIndex = len(items) - 1
		}
		commitMode = "legacy-batch-limit"
	}

	hasMore := committedIndex < len(items)-1
	checkpoint[date] = checkpointEntry{LastIndex: committedIndex}
	if err := writeCheckpoint(dir, checkpoint); err != nil {
		return nil, err
	}
	var nextStartIndex any
	if hasMore {
		nextStartIndex = committedIndex + 1
	}
	return map[string]any{
		"ok":             true,
		"date":           date,
		"committedIndex": committedIndex,
		"commitMode":     commitMode,
		"limit":          SyncFetchLimit,
		"hasMore":        hasMore,
		"nextStartIndex": nextStartIndex,
	}, nil
}

// safeSlice 返回 items[start:end]，对越界 / 反序做防御性夹取（对齐 JS slice 容错）。
func safeSlice(items []StoredNotification, start, end int) []StoredNotification {
	n := len(items)
	if start < 0 {
		start = 0
	}
	if start > n {
		start = n
	}
	if end > n {
		end = n
	}
	if end < start {
		end = start
	}
	return items[start:end]
}
