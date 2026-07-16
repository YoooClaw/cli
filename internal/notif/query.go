package notif

import (
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/YoooClaw/cli/internal/errs"
)

var isoRE = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}(:\d{2}(\.\d{1,3})?)?(Z|[+-]\d{2}:\d{2})$`)

// QueryOptions 是解析后的查询条件。
type QueryOptions struct {
	App              string
	Sender           string
	ConversationType string
	Keyword          string
	Client           string
	Limit            int
	FromTs           *time.Time
	ToTs             *time.Time
	FromDateKey      string
	ToDateKey        string
}

// RawQueryOpts 是命令行原始字符串参数。
type RawQueryOpts struct {
	From             string
	To               string
	App              string
	Sender           string
	ConversationType string
	Keyword          string
	Client           string
	Limit            string
}

// ParseTime 解析 ISO 8601 时间（容忍无秒/带毫秒）。
func ParseTime(value string) (time.Time, bool) {
	for _, layout := range []string{
		time.RFC3339Nano, time.RFC3339,
		"2006-01-02T15:04Z07:00", "2006-01-02T15:04:05.000Z07:00",
	} {
		if t, err := time.Parse(layout, value); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func parseIso(value, name string) (time.Time, error) {
	if !isoRE.MatchString(value) {
		return time.Time{}, errs.Newf(errs.CodeInvalidArgument, "%s 必须是 ISO 8601 时间，例如 2026-03-01T09:00:00+08:00", name)
	}
	t, ok := ParseTime(value)
	if !ok {
		return time.Time{}, errs.Newf(errs.CodeInvalidArgument, "%s 不是合法时间", name)
	}
	return t, nil
}

func parseLimit(raw string, fallback int) (int, error) {
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, errs.New(errs.CodeInvalidArgument, "--limit 必须是大于 0 的整数")
	}
	return n, nil
}

// BuildQueryOptions 校验并构造 QueryOptions。
func BuildQueryOptions(opts RawQueryOpts, defaultLimit int) (QueryOptions, error) {
	if opts.ConversationType != "" && opts.ConversationType != "group" && opts.ConversationType != "private" {
		return QueryOptions{}, errs.New(errs.CodeInvalidArgument, "--conversation-type 只能是 group 或 private")
	}
	out := QueryOptions{
		App: opts.App, Sender: opts.Sender, ConversationType: opts.ConversationType,
		Keyword: opts.Keyword, Client: opts.Client,
	}
	if opts.From != "" {
		t, err := parseIso(opts.From, "--from")
		if err != nil {
			return QueryOptions{}, err
		}
		out.FromTs = &t
		out.FromDateKey = opts.From[:10]
	}
	if opts.To != "" {
		t, err := parseIso(opts.To, "--to")
		if err != nil {
			return QueryOptions{}, err
		}
		out.ToTs = &t
		out.ToDateKey = opts.To[:10]
	}
	if out.FromTs != nil && out.ToTs != nil && out.FromTs.After(*out.ToTs) {
		return QueryOptions{}, errs.New(errs.CodeInvalidArgument, "--from 不能晚于 --to")
	}
	limit, err := parseLimit(opts.Limit, defaultLimit)
	if err != nil {
		return QueryOptions{}, err
	}
	out.Limit = limit
	return out, nil
}

// Query 收集匹配的通知；目录不存在返回空（无 daemon / 尚无数据是正常态）。
func Query(notificationsDir string, opts QueryOptions) []StoredNotification {
	if !dirExists(notificationsDir) {
		return nil
	}
	return collectMatching(notificationsDir, opts)
}

// ScanStats 是一次查询扫描的统计（对齐 TS NotificationQueryStats）。
type ScanStats struct {
	DaysScanned       int  `json:"daysScanned"`
	ItemsScanned      int  `json:"itemsScanned"`
	Matched           int  `json:"matched"`
	StoppedAfterLimit bool `json:"stoppedAfterLimit"`
}

// QueryWithStats 在收集匹配通知的同时统计扫描量，达到 limit 即早停
// （对齐 TS collectMatchingNotificationsWithStats）。
func QueryWithStats(notificationsDir string, opts QueryOptions) ([]StoredNotification, ScanStats) {
	stats := ScanStats{}
	if !dirExists(notificationsDir) {
		return nil, stats
	}
	var results []StoredNotification
	for _, dateKey := range listDateKeys(notificationsDir) {
		if opts.FromDateKey != "" && dateKey < opts.FromDateKey {
			continue
		}
		if opts.ToDateKey != "" && dateKey > opts.ToDateKey {
			continue
		}
		stats.DaysScanned++
		items := readDateFile(notificationsDir, dateKey)
		sortByTimestampDesc(items)
		for _, item := range items {
			stats.ItemsScanned++
			if !MatchesQuery(item, opts) {
				continue
			}
			stats.Matched++
			results = append(results, item)
			if len(results) >= opts.Limit {
				stats.StoppedAfterLimit = true
				return results, stats
			}
		}
	}
	sortByTimestampDesc(results)
	if len(results) > opts.Limit {
		results = results[:opts.Limit]
	}
	return results, stats
}

func dirExists(dir string) bool {
	info, err := os.Stat(dir)
	return err == nil && info.IsDir()
}

func sortByTimestampDesc(items []StoredNotification) {
	sort.SliceStable(items, func(i, j int) bool {
		ti, _ := ParseTime(items[i].Timestamp)
		tj, _ := ParseTime(items[j].Timestamp)
		return ti.After(tj)
	})
}

func collectMatching(dir string, opts QueryOptions) []StoredNotification {
	keys := listDateKeys(dir)
	var results []StoredNotification
	canStop := opts.FromTs == nil && opts.ToTs == nil

	for _, dateKey := range keys {
		if opts.FromDateKey != "" && dateKey < opts.FromDateKey {
			continue
		}
		if opts.ToDateKey != "" && dateKey > opts.ToDateKey {
			continue
		}
		items := readDateFile(dir, dateKey)
		sortByTimestampDesc(items)
		for _, item := range items {
			if !MatchesQuery(item, opts) {
				continue
			}
			results = append(results, item)
			if canStop && len(results) >= opts.Limit {
				return results
			}
		}
	}
	sortByTimestampDesc(results)
	if len(results) > opts.Limit {
		results = results[:opts.Limit]
	}
	return results
}

// MatchesQuery 判断单条通知是否命中查询条件。
func MatchesQuery(item StoredNotification, opts QueryOptions) bool {
	if opts.Client != "" && opts.Client != "all" {
		label := item.ClientLabel
		if label == "" {
			label = "legacy"
		}
		if label != opts.Client {
			return false
		}
	}
	if opts.App != "" && !MatchesAppFilter(item, opts.App) {
		return false
	}
	if opts.ConversationType != "" && item.ConversationType != opts.ConversationType {
		return false
	}
	if opts.Sender != "" {
		hay := joinNonEmpty([]string{item.SenderName, item.Title}, "\n")
		if !strings.Contains(hay, opts.Sender) {
			return false
		}
	}
	if opts.Keyword != "" {
		hay := joinNonEmpty([]string{item.Title, item.Content, item.SenderName, item.ConversationName}, "\n")
		if !strings.Contains(hay, opts.Keyword) {
			return false
		}
	}
	ts, ok := ParseTime(item.Timestamp)
	if !ok {
		return false
	}
	if opts.FromTs != nil && ts.Before(*opts.FromTs) {
		return false
	}
	if opts.ToTs != nil && ts.After(*opts.ToTs) {
		return false
	}
	return true
}

var appAliasGroups = [][]string{
	{"微信", "wechat", "weixin", "com.tencent.mm", "com.tencent.xin"},
	{"企业微信", "wecom", "wxwork", "com.tencent.wework"},
	{"飞书", "feishu", "lark", "com.ss.android.lark", "com.bytedance.ee.lark", "com.larksuite.suite"},
	{"钉钉", "dingtalk", "com.alibaba.android.rimet"},
	{"qq", "腾讯qq", "com.tencent.mobileqq"},
}

func normalizeLookup(v string) string { return strings.ToLower(strings.TrimSpace(v)) }

// appAliasGroupIndex 返回 value 所属别名组的下标，-1 表示无。
func appAliasGroupIndex(value string) int {
	n := normalizeLookup(value)
	if n == "" {
		return -1
	}
	for gi, group := range appAliasGroups {
		for _, cand := range group {
			if normalizeLookup(cand) == n {
				return gi
			}
		}
	}
	return -1
}

// MatchesAppFilter 判断通知是否匹配 --app 过滤（支持中英文别名）。
func MatchesAppFilter(item StoredNotification, app string) bool {
	filter := normalizeLookup(app)
	if filter == "" {
		return true
	}
	candidates := nonEmpty([]string{item.AppName, item.AppDisplayName})
	for _, c := range candidates {
		if normalizeLookup(c) == filter {
			return true
		}
	}
	fg := appAliasGroupIndex(app)
	if fg < 0 {
		return false
	}
	for _, c := range candidates {
		if appAliasGroupIndex(c) == fg {
			return true
		}
	}
	return false
}

func nonEmpty(values []string) []string {
	var out []string
	for _, v := range values {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

func joinNonEmpty(values []string, sep string) string {
	return strings.Join(nonEmpty(values), sep)
}
