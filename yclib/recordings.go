package yclib

import (
	"context"

	"github.com/YoooClaw/cli/internal/recording"
)

// RecordingsClient 暴露录音查询能力（纯本地：直接读 profile 的 recordings 索引，不经 daemon）。
type RecordingsClient struct {
	c *Client
}

// Recordings 返回录音能力子 client。
func (c *Client) Recordings() *RecordingsClient { return &RecordingsClient{c: c} }

// RecordingListOpts 是 List 的可选过滤。
type RecordingListOpts struct {
	Status string // 按转写/传输状态过滤
	Client string // 按 clientLabel 过滤；"all" 或空为全部
}

// List 列出录音。等价于 CLI `recording list`，返回 typed 结果（完整 Entry，含各产物文件名）。
func (r *RecordingsClient) List(ctx context.Context, opts RecordingListOpts) ([]RecordingEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := make([]RecordingEntry, 0)
	for _, rec := range recording.ReadIndex(r.c.paths.Recordings) {
		if opts.Status != "" && rec.Status != opts.Status {
			continue
		}
		if opts.Client != "" && opts.Client != "all" && labelOrLegacy(rec.ClientLabel) != opts.Client {
			continue
		}
		out = append(out, rec)
	}
	r.c.logf("yclib recordings.list total=%d", len(out))
	return out, nil
}

// Get 按 id 取单条录音详情；不存在返回 CodeNotFound。
func (r *RecordingsClient) Get(ctx context.Context, id string) (RecordingEntry, error) {
	if err := ctx.Err(); err != nil {
		return RecordingEntry{}, err
	}
	for _, rec := range recording.ReadIndex(r.c.paths.Recordings) {
		if rec.ID == id {
			return rec, nil
		}
	}
	return RecordingEntry{}, newNotFound("录音不存在：" + id)
}
