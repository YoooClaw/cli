// Package image 读取图片索引（images/index.json），对齐 TS 版 src/image/storage.ts。
//
// 元数据与同步状态由 daemon 图片通道写入；CLI 查询纯读，不需要 daemon。
package image

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/YoooClaw/cli/internal/fsutil"
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

// Logger 是图片写侧依赖的最小日志接口。
type Logger interface {
	Info(string)
	Warn(string)
}

var writeMu sync.Mutex

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

func writeIndex(imagesDir string, entries []Entry) error {
	return fsutil.WriteJSON(filepath.Join(imagesDir, "index.json"), struct {
		Images []Entry `json:"images"`
	}{Images: entries}, fsutil.ConfigFileMode)
}

func upsert(imagesDir string, entry Entry) error {
	writeMu.Lock()
	defer writeMu.Unlock()
	entries := ReadIndex(imagesDir)
	idx := -1
	for i := range entries {
		if entries[i].ImageID == entry.ImageID {
			idx = i
			break
		}
	}
	if idx >= 0 {
		entries[idx] = entry
	} else {
		entries = append(entries, entry)
	}
	if err := fsutil.EnsureDir(imagesDir, fsutil.DirMode); err != nil {
		return err
	}
	return writeIndex(imagesDir, entries)
}

// SyncPayload 是 images.sync / POST /images 的请求体。
type SyncPayload struct {
	ImageID string   `json:"imageId"`
	Image   Metadata `json:"image"`
}

// IngestResult 是图片同步即时返回。
type IngestResult struct {
	OK      bool   `json:"ok"`
	ImageID string `json:"imageId"`
	Status  string `json:"status"`
	Deduped bool   `json:"deduped,omitempty"`
}

// Ingest 接收图片元数据，后台下载 OSS 文件。
func Ingest(imagesDir string, payload SyncPayload, clientLabel string, maxBytes int64, logger Logger) (IngestResult, error) {
	imageID := strings.TrimSpace(payload.ImageID)
	if imageID == "" || strings.TrimSpace(payload.Image.OssImageURL) == "" || strings.TrimSpace(payload.Image.CreatedAt) == "" {
		return IngestResult{}, fmt.Errorf("imageId / image.oss_image_url / image.created_at 必填")
	}
	if clientLabel == "" {
		clientLabel = "default"
	}
	for _, existing := range ReadIndex(imagesDir) {
		if existing.ImageID == imageID && existing.Metadata.OssImageURL == payload.Image.OssImageURL && existing.Status == "synced" {
			return IngestResult{OK: true, ImageID: imageID, Status: "synced", Deduped: true}, nil
		}
	}
	entry := Entry{
		ImageID: imageID, ClientLabel: clientLabel, Metadata: payload.Image,
		Status: "syncing", LastError: nil, SyncedAt: nil,
	}
	if err := upsert(imagesDir, entry); err != nil {
		return IngestResult{}, err
	}
	go func() {
		if err := downloadInBackground(imagesDir, entry, maxBytes, logger); err != nil {
			logger.Warn("image[" + imageID + "] 后台下载异常：" + err.Error())
		}
	}()
	return IngestResult{OK: true, ImageID: imageID, Status: "syncing"}, nil
}

func downloadInBackground(imagesDir string, entry Entry, maxBytes int64, logger Logger) error {
	filesDir := filepath.Join(imagesDir, "files")
	if err := fsutil.EnsureDir(filesDir, fsutil.DirMode); err != nil {
		return err
	}
	if maxBytes <= 0 {
		maxBytes = 20 * 1024 * 1024
	}
	relative := filepath.ToSlash(filepath.Join("files", entry.ImageID+"."+extFromMime(entry.Metadata.MimeType)))
	dest := filepath.Join(imagesDir, relative)
	err := downloadImage(entry.Metadata.OssImageURL, dest, maxBytes)
	if err != nil {
		msg := err.Error()
		entry.Status = "sync_failed"
		entry.LastError = &msg
		entry.LocalFile = nil
		_ = upsert(imagesDir, entry)
		logger.Warn("image[" + entry.ImageID + "] 下载失败：" + msg)
		return nil
	}
	entry.LocalFile = &relative
	entry.Status = "synced"
	entry.LastError = nil
	now := time.Now().UTC().Format(time.RFC3339Nano)
	entry.SyncedAt = &now
	if err := upsert(imagesDir, entry); err != nil {
		return err
	}
	logger.Info("image[" + entry.ImageID + "] 下载完成")
	return nil
}

func downloadImage(rawURL, dest string, maxBytes int64) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("OSS 返回 %d", resp.StatusCode)
	}
	if resp.ContentLength > 0 && resp.ContentLength > maxBytes {
		return fmt.Errorf("图片超过上限 %d 字节", maxBytes)
	}
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	written, err := io.Copy(f, io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		_ = os.Remove(dest)
		return err
	}
	if written > maxBytes {
		_ = os.Remove(dest)
		return fmt.Errorf("图片超过上限 %d 字节", maxBytes)
	}
	return nil
}

func extFromMime(mime string) string {
	mime = strings.ToLower(mime)
	switch {
	case strings.Contains(mime, "png"):
		return "png"
	case strings.Contains(mime, "webp"):
		return "webp"
	case strings.Contains(mime, "gif"):
		return "gif"
	default:
		return "jpg"
	}
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
