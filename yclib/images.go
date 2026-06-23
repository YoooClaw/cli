package yclib

import (
	"context"

	"github.com/YoooClaw/cli/internal/image"
	"github.com/YoooClaw/cli/internal/notif"
)

// ImagesClient 暴露图片查询能力（纯本地：直接读 profile 的 images 索引，不经 daemon）。
type ImagesClient struct {
	c *Client
}

// Images 返回图片能力子 client。
func (c *Client) Images() *ImagesClient { return &ImagesClient{c: c} }

// ImageListOpts 是 List 的可选过滤；Limit <= 0 时默认 100。
type ImageListOpts struct {
	Status string // 按同步状态过滤
	Client string // 按 clientLabel 过滤；"all" 或空为全部
	App    string // 按来源应用过滤（支持别名）
	From   string // createdAt 下界（字符串字典序比较）
	To     string // createdAt 上界
	Limit  int
}

// List 列出图片（按创建时间倒序）。等价于 CLI `image list`，返回 typed 结果。
func (i *ImagesClient) List(ctx context.Context, opts ImageListOpts) ([]ImageEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := make([]ImageEntry, 0)
	for _, it := range image.ReadIndex(i.c.paths.Images) {
		if opts.Status != "" && it.Status != opts.Status {
			continue
		}
		if opts.Client != "" && opts.Client != "all" && labelOrLegacy(it.ClientLabel) != opts.Client {
			continue
		}
		if opts.App != "" && !notif.MatchesAppFilter(notif.StoredNotification{AppName: it.Metadata.SourceApp}, opts.App) {
			continue
		}
		if opts.From != "" && it.Metadata.CreatedAt < opts.From {
			continue
		}
		if opts.To != "" && it.Metadata.CreatedAt > opts.To {
			continue
		}
		out = append(out, it)
	}
	image.SortByCreatedDesc(out)
	limit := opts.Limit
	if limit <= 0 {
		limit = 100
	}
	if len(out) > limit {
		out = out[:limit]
	}
	i.c.logf("yclib images.list total=%d", len(out))
	return out, nil
}

// Get 按 imageId 取单张图片详情；不存在返回 CodeNotFound。
func (i *ImagesClient) Get(ctx context.Context, imageID string) (ImageEntry, error) {
	if err := ctx.Err(); err != nil {
		return ImageEntry{}, err
	}
	for _, it := range image.ReadIndex(i.c.paths.Images) {
		if it.ImageID == imageID {
			return it, nil
		}
	}
	return ImageEntry{}, newNotFound("图片不存在：" + imageID)
}
