package yclib

import (
	"context"

	"github.com/YoooClaw/cli/internal/notif"
)

// defaultSearchLimit 对齐 CLI `notification search` 的默认上限（100）。
const defaultSearchLimit = 100

// NotificationsClient 暴露通知查询能力（纯本地：直接读 profile 的 notifications 目录，
// 不经 daemon）。经 Client.Notifications() 获取。
type NotificationsClient struct {
	c *Client
}

// Notifications 返回通知能力子 client。
func (c *Client) Notifications() *NotificationsClient {
	return &NotificationsClient{c: c}
}

// SearchResult 是 Search 的类型化结果。Notifications 按时间倒序，Stats 给出本次
// 扫描的统计（扫描天数 / 条数 / 命中数 / 是否因 limit 早停）。
type SearchResult struct {
	Notifications []StoredNotification `json:"notifications"`
	Stats         ScanStats            `json:"stats"`
}

// Search 按筛选条件查询通知。等价于 CLI `notification search`，但返回类型化结果、
// 不写 stdout、不 os.Exit。opts 复用底层 notif.RawQueryOpts（字符串原始参数，
// 与 CLI flag 同语义）。
//
// 校验失败（如 conversation-type 非法、from 晚于 to）返回 *yclib.Error，
// 可用 ErrorCode(err) == CodeInvalidArgument 判别。notifications 目录不存在
// （无 daemon / 尚无数据）视为正常态，返回空结果而非错误。
func (n *NotificationsClient) Search(ctx context.Context, opts RawQueryOpts) (SearchResult, error) {
	if err := ctx.Err(); err != nil {
		return SearchResult{}, err
	}
	qo, err := notif.BuildQueryOptions(opts, defaultSearchLimit)
	if err != nil {
		return SearchResult{}, err
	}
	items, stats := notif.QueryWithStats(n.c.paths.Notifications, qo)
	n.c.logf("yclib notifications.search matched=%d scanned=%d", stats.Matched, stats.ItemsScanned)
	return SearchResult{Notifications: items, Stats: stats}, nil
}
