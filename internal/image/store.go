// Package image 读取图片索引（images/index.json），对齐 TS 版 src/image/storage.ts。
//
// 元数据与同步状态由 daemon 图片通道写入；CLI 查询纯读，不需要 daemon。
package image

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
)

// Metadata 是图片元数据。
type Metadata struct {
	OssImageURL string `json:"oss_image_url"`
	CreatedAt   string `json:"created_at"`
	MimeType    string `json:"mime_type,omitempty"`
	Width       int    `json:"width,omitempty"`
	Height      int    `json:"height,omitempty"`
	SizeBytes   int64  `json:"size_bytes,omitempty"`
	SourceApp   string `json:"source_app,omitempty"`
	Caption     string `json:"caption,omitempty"`
}

// Entry 是一条图片索引项。
type Entry struct {
	ImageID     string   `json:"imageId"`
	ClientLabel string   `json:"clientLabel,omitempty"`
	Metadata    Metadata `json:"metadata"`
	LocalFile   *string  `json:"localFile,omitempty"`
	Thumbnail   *string  `json:"thumbnail,omitempty"`
	Status      string   `json:"status"`
	LastError   *string  `json:"lastError,omitempty"`
	SyncedAt    *string  `json:"syncedAt,omitempty"`
}

// ReadIndex 读取 images/index.json 的 images[]；不存在返回空。
func ReadIndex(imagesDir string) []Entry {
	raw, err := os.ReadFile(filepath.Join(imagesDir, "index.json"))
	if err != nil {
		return nil
	}
	var wrapper struct {
		Images []Entry `json:"images"`
	}
	if json.Unmarshal(raw, &wrapper) != nil {
		return nil
	}
	return wrapper.Images
}

// ResolveFile 把相对路径解析为绝对路径。
func ResolveFile(imagesDir, relative string) string {
	if filepath.IsAbs(relative) {
		return relative
	}
	return filepath.Join(imagesDir, relative)
}

// SortByCreatedDesc 按 created_at 倒序排序。
func SortByCreatedDesc(entries []Entry) {
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].Metadata.CreatedAt > entries[j].Metadata.CreatedAt
	})
}
