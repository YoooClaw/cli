package yclib

import (
	"context"

	"github.com/YoooClaw/cli/internal/notif"
)

// maxAggLimit 充当聚合的“全部”上限（对齐 CLI 的 maxLimit 语义）。
const maxAggLimit = 1 << 60

// SummaryOpts 是 Summary 的输入。Query 复用 search 的原始过滤参数；Sample/Top
// 控制返回的样例条数与聚合榜单长度（<=0 时取默认 30 / 10）。
type SummaryOpts struct {
	Query  RawQueryOpts
	Sample int
	Top    int
}

// SummaryResult 是 Summary 的类型化结果：范围内通知总数 + 应用/发送人 Top 榜 + 最近样例。
type SummaryResult struct {
	Total      int                  `json:"total"`
	From       string               `json:"from,omitempty"`
	To         string               `json:"to,omitempty"`
	TopApps    []Count              `json:"topApps"`
	TopSenders []Count              `json:"topSenders"`
	Sample     []StoredNotification `json:"sample"`
}

// Summary 聚合统计 + 样例摘要，供 Agent 总结。等价于 CLI `notification summary`，
// 返回类型化结果。纯本地、不经 daemon。
func (n *NotificationsClient) Summary(ctx context.Context, opts SummaryOpts) (SummaryResult, error) {
	if err := ctx.Err(); err != nil {
		return SummaryResult{}, err
	}
	sample, top := opts.Sample, opts.Top
	if sample <= 0 {
		sample = 30
	}
	if top <= 0 {
		top = 10
	}
	raw := opts.Query
	// 聚合范围内全部通知（Query.Limit 为空时 BuildQueryOptions 取 maxAggLimit 默认；
	// 调用方显式给了正数 Limit 则尊重之）。
	qo, err := notif.BuildQueryOptions(raw, maxAggLimit)
	if err != nil {
		return SummaryResult{}, err
	}
	items := notif.Query(n.c.paths.Notifications, qo)
	n.c.logf("yclib notifications.summary total=%d", len(items))
	return SummaryResult{
		Total:      len(items),
		From:       raw.From,
		To:         raw.To,
		TopApps:    notif.TopCounts(items, notif.AppLabel, top),
		TopSenders: notif.TopCounts(items, notif.SenderLabel, top),
		Sample:     sliceN(items, sample),
	}, nil
}

// StatsOpts 是 Stats 的输入。From/To 接受 "YYYY-MM-DD" 或 ISO 8601（空 → 默认近 7 天）；
// Dim 取 date|app|sender|hour|client|all（空 → all）；App/Sender/Client 为可选过滤。
type StatsOpts struct {
	From   string
	To     string
	Dim    string
	App    string
	Sender string
	Client string
}

// StatsResult 是 Stats 的类型化结果。各维度切片仅在被请求（dim=all 或匹配 Dim）时填充。
type StatsResult struct {
	Total  int     `json:"total"`
	From   string  `json:"from"`
	To     string  `json:"to"`
	Dim    string  `json:"dim"`
	Date   []Count `json:"date,omitempty"`
	App    []Count `json:"app,omitempty"`
	Sender []Count `json:"sender,omitempty"`
	Hour   []Count `json:"hour,omitempty"`
	Client []Count `json:"client,omitempty"`
}

var statsDims = map[string]bool{"date": true, "app": true, "sender": true, "hour": true, "client": true, "all": true}

// Stats 按维度聚合统计。等价于 CLI `notification stats`，返回类型化结果。纯本地、不经 daemon。
// 非法 Dim / 时间边界返回 *yclib.Error（CodeInvalidArgument）。
func (n *NotificationsClient) Stats(ctx context.Context, opts StatsOpts) (StatsResult, error) {
	if err := ctx.Err(); err != nil {
		return StatsResult{}, err
	}
	fromRaw, toRaw := opts.From, opts.To
	if fromRaw == "" {
		fromRaw = localDaysAgo(7)
	}
	if toRaw == "" {
		toRaw = localToday()
	}
	from, err := notif.ParseStatsBoundary(fromRaw, "from")
	if err != nil {
		return StatsResult{}, err
	}
	to, err := notif.ParseStatsBoundary(toRaw, "to")
	if err != nil {
		return StatsResult{}, err
	}
	if from.StartTs.After(to.EndTs) {
		return StatsResult{}, newInvalidArg("from 不能晚于 to")
	}
	dim := opts.Dim
	if dim == "" {
		dim = "all"
	}
	if !statsDims[dim] {
		return StatsResult{}, newInvalidArg("dim 只能是 date|app|sender|hour|client|all")
	}
	qo := notif.QueryOptions{
		App: opts.App, Sender: opts.Sender, Client: opts.Client,
		Limit: maxAggLimit, FromTs: from.ExactTs, ToTs: to.ExactTs,
		FromDateKey: from.MinDateKey, ToDateKey: to.MaxDateKey,
	}
	items := notif.Query(n.c.paths.Notifications, qo)
	n.c.logf("yclib notifications.stats total=%d dim=%s", len(items), dim)

	res := StatsResult{Total: len(items), From: from.Raw, To: to.Raw, Dim: dim}
	want := func(d string) bool { return dim == "all" || dim == d }
	if want("date") {
		res.Date = notif.TopCounts(items, func(s notif.StoredNotification) string { return notif.TsLocalDate(s.Timestamp) }, maxAggLimit)
	}
	if want("app") {
		res.App = notif.TopCounts(items, notif.AppLabel, maxAggLimit)
	}
	if want("sender") {
		res.Sender = notif.TopCounts(items, notif.SenderLabel, maxAggLimit)
	}
	if want("hour") {
		res.Hour = notif.TopCounts(items, func(s notif.StoredNotification) string { return notif.TsLocalHour(s.Timestamp) }, maxAggLimit)
	}
	if want("client") {
		res.Client = notif.TopCounts(items, notif.ClientLabelOf, maxAggLimit)
	}
	return res, nil
}
