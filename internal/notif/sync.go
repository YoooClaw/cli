package notif

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"github.com/YoooClaw/cli/internal/errs"
	"github.com/YoooClaw/cli/internal/fsutil"
)

// SyncFetchLimit 单批同步上限（对齐 TS SYNC_FETCH_LIMIT）。
// 刻意调小到 100，使每个批次（模型阅读+总结+写记忆）能在数十秒内完成，
// 让前台/后台都能频繁提交 checkpoint 并快速给用户进度反馈，避免单回合塞太多导致超时卡死。
const SyncFetchLimit = 100

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

// SyncScope 描述一次扫描/同步的日期范围（对齐 TS SyncScanScope）。
type SyncScope struct {
	Mode     string `json:"mode"` // all | today | date | range
	FromDate string `json:"fromDate,omitempty"`
	ToDate   string `json:"toDate,omitempty"`
}

// SyncScopeOptions 是 scope 的原始 CLI 入参。
type SyncScopeOptions struct {
	All      bool
	Date     string
	FromDate string
	ToDate   string
}

var dateKeyRE = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})$`)

// localToday 返回本地时区当天日期 YYYY-MM-DD（与落盘 dateKey 同语义）。
func localToday() string {
	return time.Now().In(time.Local).Format("2006-01-02")
}

func validateDateKey(value, optionName string) error {
	m := dateKeyRE.FindStringSubmatch(value)
	if m == nil {
		return errs.Newf(errs.CodeInvalidArgument, "%s 必须是 YYYY-MM-DD 格式", optionName)
	}
	if _, err := time.Parse("2006-01-02", value); err != nil {
		return errs.Newf(errs.CodeInvalidArgument, "%s 不是合法日期", optionName)
	}
	return nil
}

// ResolveScanScope 把 CLI 入参解析为 SyncScope（缺省为本地当天，对齐 TS resolveScanScope）。
func ResolveScanScope(opts SyncScopeOptions) (SyncScope, error) {
	hasRange := opts.FromDate != "" || opts.ToDate != ""
	if opts.All && (opts.Date != "" || hasRange) {
		return SyncScope{}, errs.New(errs.CodeInvalidArgument, "--all 不能与日期范围参数同时使用")
	}
	if opts.Date != "" && hasRange {
		return SyncScope{}, errs.New(errs.CodeInvalidArgument, "--date 不能与日期范围参数同时使用")
	}

	if opts.All {
		return SyncScope{Mode: "all"}, nil
	}

	if opts.Date != "" {
		if err := validateDateKey(opts.Date, "--date"); err != nil {
			return SyncScope{}, err
		}
		return SyncScope{Mode: "date", FromDate: opts.Date, ToDate: opts.Date}, nil
	}

	if hasRange {
		if opts.FromDate != "" {
			if err := validateDateKey(opts.FromDate, "--from-date"); err != nil {
				return SyncScope{}, err
			}
		}
		if opts.ToDate != "" {
			if err := validateDateKey(opts.ToDate, "--to-date"); err != nil {
				return SyncScope{}, err
			}
		}
		if opts.FromDate != "" && opts.ToDate != "" && opts.FromDate > opts.ToDate {
			return SyncScope{}, errs.New(errs.CodeInvalidArgument, "--from-date 不能晚于 --to-date")
		}
		return SyncScope{Mode: "range", FromDate: opts.FromDate, ToDate: opts.ToDate}, nil
	}

	current := localToday()
	return SyncScope{Mode: "today", FromDate: current, ToDate: current}, nil
}

func isDateInScope(dateKey string, scope SyncScope) bool {
	if scope.Mode == "all" {
		return true
	}
	if scope.FromDate != "" && dateKey < scope.FromDate {
		return false
	}
	if scope.ToDate != "" && dateKey > scope.ToDate {
		return false
	}
	return true
}

// SyncPending 是 scan 返回的单个日期待同步摘要。
type SyncPending struct {
	Date       string `json:"date"`
	Count      int    `json:"count"`
	StartIndex int    `json:"startIndex"`
}

// ScanSync 扫描各日期未处理通知，返回范围内待同步摘要（缺省只看本地当天）。
func ScanSync(dir string, scope SyncScope) map[string]any {
	checkpoint := readCheckpoint(dir)
	pending := []SyncPending{}
	totalPending := 0
	outsideScopePending := 0
	for _, dateKey := range listDateKeys(dir) {
		items := readDateFile(dir, dateKey)
		lastIndex := lastIndexFor(checkpoint, dateKey)
		unprocessed := len(items) - (lastIndex + 1)
		if unprocessed <= 0 {
			continue
		}
		if isDateInScope(dateKey, scope) {
			pending = append(pending, SyncPending{Date: dateKey, Count: unprocessed, StartIndex: lastIndex + 1})
			totalPending += unprocessed
		} else {
			outsideScopePending += unprocessed
		}
	}
	return map[string]any{
		"ok":                   true,
		"scope":                scope,
		"pending":              pending,
		"totalPending":         totalPending,
		"outsideScopePending":  outsideScopePending,
		"allDatesTotalPending": totalPending + outsideScopePending,
	}
}

// NextSync 是通用批次迭代器：返回范围内下一批未处理通知（≤SyncFetchLimit 条）及
// 配套的 commitCommand，供任意下游处理；全部处理完时返回 done=true。
// 不推进 checkpoint，由调用方在处理完本批后执行 commitCommand。
func NextSync(dir string, scope SyncScope) map[string]any {
	checkpoint := readCheckpoint(dir)

	totalPending := 0
	outsideScopePending := 0
	nextDate := ""
	var nextItems []StoredNotification
	nextStartIndex := 0

	for _, dateKey := range listDateKeys(dir) { // 降序：从最新日期开始处理
		items := readDateFile(dir, dateKey)
		lastIndex := lastIndexFor(checkpoint, dateKey)
		unprocessed := len(items) - (lastIndex + 1)
		if unprocessed <= 0 {
			continue
		}
		if !isDateInScope(dateKey, scope) {
			outsideScopePending += unprocessed
			continue
		}
		totalPending += unprocessed
		if nextDate == "" {
			nextDate = dateKey
			nextItems = items
			nextStartIndex = lastIndex + 1
		}
	}

	if nextDate == "" {
		return map[string]any{
			"ok":                   true,
			"done":                 true,
			"scope":                scope,
			"totalPending":         0,
			"outsideScopePending":  outsideScopePending,
			"allDatesTotalPending": outsideScopePending,
		}
	}

	notifications := safeSlice(nextItems, nextStartIndex, nextStartIndex+SyncFetchLimit)
	endIndex := nextStartIndex + len(notifications) - 1
	unprocessedInDate := len(nextItems) - nextStartIndex

	return map[string]any{
		"ok":                  true,
		"done":                false,
		"scope":               scope,
		"date":                nextDate,
		"startIndex":          nextStartIndex,
		"endIndex":            endIndex,
		"returned":            len(notifications),
		"hasMoreInDate":       unprocessedInDate > len(notifications),
		"remainingInScope":    totalPending - len(notifications),
		"outsideScopePending": outsideScopePending,
		"commitCommand":       "yoooclaw sync commit --date " + nextDate + " --end-index " + strconv.Itoa(endIndex),
		"notifications":       notifications,
	}
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
