// Package webpage 接收浏览器扩展投递的网页正文并落成本地 Markdown。
//
// 目录结构（与 images/ 同构，三端共享同一份 index.json）：
//
//	<profile>/web-pages/
//	├── files/
//	│   ├── 3f2a91c4-array-prototype-map.md    # YAML frontmatter + 正文
//	│   └── 3f2a91c4-array-prototype-map.html  # 原始 HTML 存档（可选 sidecar）
//	└── index.json
//
// 文件名由 sha256(canonicalUrl) 的前 8 位决定，重收即覆盖同一文件——
// 去重靠文件系统，不需要额外逻辑。契约见
// yc-web-extension/docs/page-context-design.md §2 / §4.1。
package webpage

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/YoooClaw/cli/internal/fsutil"
)

const (
	// MaxMarkdownBytes 是单篇正文上限。上限来自 Relay 帧尺寸而不是计费
	// （page-context-design.md §3.5）：内层 payload 是 JSON 字符串，
	// 中途还要过 openclaw-service，不宜贴边。
	MaxMarkdownBytes = 512 * 1024
	// MaxArchiveBytes 是 HTML 存档解压后的上限。压缩比 7~11 倍的正常页面
	// 远低于此；设上限是为了挡住 gzip 炸弹。
	MaxArchiveBytes = 8 * 1024 * 1024

	filesDirName  = "files"
	indexFileName = "index.json"
	// ArchiveEncoding 是唯一接受的存档编码。
	ArchiveEncoding = "gzip+base64"
)

var writeMu sync.Mutex

// Archive 是随正文同帧投递的原始 HTML 存档（page-context-design.md §2.5）。
type Archive struct {
	Encoding string `json:"encoding"`
	Data     string `json:"data"`
	Bytes    int64  `json:"bytes,omitempty"`
}

// Payload 是 POST /web-pages 的请求体。
type Payload struct {
	URL           string   `json:"url"`
	CanonicalURL  string   `json:"canonicalUrl"`
	Title         string   `json:"title"`
	SiteName      string   `json:"siteName"`
	Byline        string   `json:"byline"`
	Language      string   `json:"language"`
	PublishedTime string   `json:"publishedTime"`
	CapturedAt    string   `json:"capturedAt"`
	Markdown      string   `json:"markdown"`
	ContentHash   string   `json:"contentHash"`
	Truncated     bool     `json:"truncated"`
	LowConfidence bool     `json:"lowConfidence"`
	Archive       *Archive `json:"archive,omitempty"`
}

// Entry 是 index.json 里的一条网页记录。字段与 TS / Python 两端严格对齐。
type Entry struct {
	URLHash         string `json:"urlHash"`
	CanonicalURL    string `json:"canonicalUrl"`
	URL             string `json:"url,omitempty"`
	Title           string `json:"title"`
	SiteName        string `json:"siteName,omitempty"`
	RelativePath    string `json:"relativePath"`
	CapturedAt      string `json:"capturedAt"`
	FirstCapturedAt string `json:"firstCapturedAt"`
	CaptureCount    int    `json:"captureCount"`
	ContentHash     string `json:"contentHash,omitempty"`
	Bytes           int    `json:"bytes"`
	ClientLabel     string `json:"clientLabel,omitempty"`
	ArchivePath     string `json:"archivePath,omitempty"`
	ArchiveBytes    int    `json:"archiveBytes,omitempty"`
}

// IngestResult 是 POST /web-pages 的响应体。
type IngestResult struct {
	OK           bool   `json:"ok"`
	URLHash      string `json:"urlHash"`
	RelativePath string `json:"relativePath"`
	ArchivePath  string `json:"archivePath,omitempty"`
	Replaced     bool   `json:"replaced"`
	CaptureCount int    `json:"captureCount"`
}

// Logger 是写侧依赖的最小日志接口。
type Logger interface {
	Info(string)
	Warn(string)
}

// Error 带上 HTTP 状态码，让 daemon 能把「参数错」与「超上限」分开回。
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }

func invalid(format string, args ...any) *Error {
	return &Error{Status: 400, Code: "INVALID_PARAMS", Message: fmt.Sprintf(format, args...)}
}

// Ingest 落盘一篇网页：规范化 URL → 定文件名 → 写 Markdown → 更新索引。
//
// 存档解压/校验失败时 Markdown 照常落盘并返回成功，只是不回 archivePath——
// 存档是重处理原料，永远不能让一次收藏失败（§4.1）。
func Ingest(dir string, payload Payload, clientLabel string, logger Logger) (IngestResult, error) {
	source := strings.TrimSpace(payload.CanonicalURL)
	if source == "" {
		source = strings.TrimSpace(payload.URL)
	}
	if source == "" {
		return IngestResult{}, invalid("canonicalUrl 必填")
	}
	canonical, err := NormalizeURL(source)
	if err != nil {
		return IngestResult{}, invalid("canonicalUrl 非法：%v", err)
	}
	markdown := payload.Markdown
	if strings.TrimSpace(markdown) == "" {
		return IngestResult{}, invalid("markdown 必填")
	}
	if len(markdown) > MaxMarkdownBytes {
		return IngestResult{}, &Error{
			Status:  413,
			Code:    "PAYLOAD_TOO_LARGE",
			Message: fmt.Sprintf("markdown %d 字节超过上限 %d，请在扩展侧按段落截断", len(markdown), MaxMarkdownBytes),
		}
	}
	if clientLabel == "" {
		clientLabel = "default"
	}
	capturedAt := strings.TrimSpace(payload.CapturedAt)
	if capturedAt == "" {
		capturedAt = time.Now().Format(time.RFC3339)
	}

	sum := sha256.Sum256([]byte(canonical))
	urlHash := hex.EncodeToString(sum[:])
	relativePath := filesDirName + "/" + urlHash[:8] + "-" + Slug(payload.Title) + ".md"

	writeMu.Lock()
	defer writeMu.Unlock()

	entries := ReadIndex(dir)
	previous, previousIdx := findByHash(entries, urlHash)

	filesDir := filepath.Join(dir, filesDirName)
	if err := fsutil.EnsureDir(filesDir, fsutil.DirMode); err != nil {
		return IngestResult{}, err
	}

	// 存档先写：它决定 frontmatter 里的 archive: 一行。失败只丢存档。
	archivePath, archiveBytes := "", 0
	if payload.Archive != nil {
		html, archiveErr := DecodeArchive(*payload.Archive)
		if archiveErr != nil {
			logger.Warn("web-page[" + urlHash[:8] + "] 存档丢弃：" + archiveErr.Error())
		} else {
			candidate := strings.TrimSuffix(relativePath, ".md") + ".html"
			if err := fsutil.WriteAtomic(filepath.Join(dir, candidate), html, fsutil.SecretFileMode); err != nil {
				logger.Warn("web-page[" + urlHash[:8] + "] 存档写入失败：" + err.Error())
			} else {
				archivePath, archiveBytes = candidate, len(html)
			}
		}
	} else if previous != nil && previous.ArchivePath != "" {
		// 本次没带存档：保留上次的，但要跟着新文件名走。
		renamed := strings.TrimSuffix(relativePath, ".md") + ".html"
		if renamed == previous.ArchivePath || renameFile(dir, previous.ArchivePath, renamed) == nil {
			archivePath, archiveBytes = renamed, previous.ArchiveBytes
		}
	}

	// 0600：网页正文可能含登录后才可见的个人内容（站内信、账号页、内部 wiki），
	// 与通知/录音同一数据类别，不按普通配置文件的 0644 落盘。Python 端的
	// mkstemp 本来就是 0600，这里对齐。
	document := BuildDocument(payload, canonical, capturedAt, clientLabel, archivePath)
	if err := fsutil.WriteAtomic(filepath.Join(dir, relativePath), []byte(document), fsutil.SecretFileMode); err != nil {
		return IngestResult{}, err
	}

	entry := Entry{
		URLHash:         urlHash,
		CanonicalURL:    canonical,
		URL:             strings.TrimSpace(payload.URL),
		Title:           payload.Title,
		SiteName:        strings.TrimSpace(payload.SiteName),
		RelativePath:    relativePath,
		CapturedAt:      capturedAt,
		FirstCapturedAt: capturedAt,
		CaptureCount:    1,
		ContentHash:     strings.TrimSpace(payload.ContentHash),
		Bytes:           len(document),
		ClientLabel:     clientLabel,
		ArchivePath:     archivePath,
		ArchiveBytes:    archiveBytes,
	}
	replaced := previous != nil
	if replaced {
		entry.FirstCapturedAt = previous.FirstCapturedAt
		if entry.FirstCapturedAt == "" {
			entry.FirstCapturedAt = capturedAt
		}
		entry.CaptureCount = previous.CaptureCount + 1
		// 标题改了会算出新文件名；删掉旧的，保证一个 URL 只有一个文件。
		if previous.RelativePath != "" && previous.RelativePath != relativePath {
			removeFile(dir, previous.RelativePath)
		}
		if previous.ArchivePath != "" && previous.ArchivePath != archivePath {
			removeFile(dir, previous.ArchivePath)
		}
		entries[previousIdx] = entry
	} else {
		entries = append(entries, entry)
	}
	if err := writeIndex(dir, entries); err != nil {
		return IngestResult{}, err
	}

	logger.Info(fmt.Sprintf("web-page[%s] 已落盘：%s（第 %d 次收藏）", urlHash[:8], relativePath, entry.CaptureCount))
	return IngestResult{
		OK:           true,
		URLHash:      urlHash,
		RelativePath: relativePath,
		ArchivePath:  archivePath,
		Replaced:     replaced,
		CaptureCount: entry.CaptureCount,
	}, nil
}

// DecodeArchive 解出原始 HTML 存档：base64 → gunzip → 校验是 UTF-8 文本且以 < 开头。
// 接收端不做任何 HTML 清洗——清洗全在扩展端（只有那里有活的 DOM）。
func DecodeArchive(archive Archive) ([]byte, error) {
	if archive.Encoding != ArchiveEncoding {
		return nil, fmt.Errorf("不支持的存档编码 %q（只接受 %s）", archive.Encoding, ArchiveEncoding)
	}
	compressed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(archive.Data))
	if err != nil {
		return nil, fmt.Errorf("base64 解码失败：%w", err)
	}
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return nil, fmt.Errorf("gzip 解压失败：%w", err)
	}
	defer reader.Close()
	// 多读 1 字节用于判断是否超限：压缩包很小但解压后极大的输入必须挡在写盘之前。
	html, err := io.ReadAll(io.LimitReader(reader, MaxArchiveBytes+1))
	if err != nil {
		return nil, fmt.Errorf("gzip 解压失败：%w", err)
	}
	if len(html) > MaxArchiveBytes {
		return nil, fmt.Errorf("存档解压后超过上限 %d 字节", MaxArchiveBytes)
	}
	if !utf8.Valid(html) {
		return nil, fmt.Errorf("存档不是合法的 UTF-8 文本")
	}
	if !strings.HasPrefix(strings.TrimLeft(string(html), " \t\r\n\ufeff"), "<") {
		return nil, fmt.Errorf("存档不像 HTML（未以 < 开头）")
	}
	return html, nil
}

// Status 回答「这些 URL 收过没有」。入参可以是完整 hash，也可以是扩展本地
// 索引里那种 16 位前缀（§3.4：本地索引存 hash 不存明文 URL）。
func Status(dir string, hashes []string) map[string]map[string]string {
	saved := map[string]map[string]string{}
	entries := ReadIndex(dir)
	for _, raw := range hashes {
		query := strings.ToLower(strings.TrimSpace(raw))
		// 太短的前缀会命中一大片，不如不答。
		if len(query) < 8 {
			continue
		}
		for _, entry := range entries {
			if strings.HasPrefix(strings.ToLower(entry.URLHash), query) {
				saved[raw] = map[string]string{"capturedAt": entry.CapturedAt}
				break
			}
		}
	}
	return saved
}

// ReadIndex 读取 web-pages/index.json 的 pages[]；不存在返回空。
func ReadIndex(dir string) []Entry {
	raw, err := os.ReadFile(filepath.Join(dir, indexFileName))
	if err != nil {
		return nil
	}
	var wrapper struct {
		Pages []Entry `json:"pages"`
	}
	if json.Unmarshal(raw, &wrapper) != nil {
		return nil
	}
	return wrapper.Pages
}

// SortByCapturedDesc 按收藏时间倒序排序。
func SortByCapturedDesc(entries []Entry) {
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].CapturedAt > entries[j].CapturedAt
	})
}

// ProjectFields 按 ?fields= 裁剪索引条目。fields 为空时返回全字段。
// 扩展登录后的一次性回填只要 hash + capturedAt，没必要把全量正文元数据传回去。
func ProjectFields(entries []Entry, fields []string) []map[string]any {
	wanted := map[string]bool{}
	for _, f := range fields {
		if f = strings.TrimSpace(f); f != "" {
			wanted[f] = true
		}
	}
	out := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		full := map[string]any{
			"urlHash": entry.URLHash, "canonicalUrl": entry.CanonicalURL, "url": entry.URL,
			"title": entry.Title, "siteName": entry.SiteName, "relativePath": entry.RelativePath,
			"capturedAt": entry.CapturedAt, "firstCapturedAt": entry.FirstCapturedAt,
			"captureCount": entry.CaptureCount, "contentHash": entry.ContentHash,
			"bytes": entry.Bytes, "clientLabel": entry.ClientLabel,
			"archivePath": entry.ArchivePath, "archiveBytes": entry.ArchiveBytes,
		}
		if len(wanted) == 0 {
			out = append(out, full)
			continue
		}
		item := map[string]any{}
		for name, value := range full {
			// hash 是 urlHash 的别名：契约里写的是 ?fields=hash,capturedAt。
			if wanted[name] || (name == "urlHash" && wanted["hash"]) {
				item[name] = value
			}
		}
		out = append(out, item)
	}
	return out
}

// ResolveFile 把索引里的相对路径解析为绝对路径。
func ResolveFile(dir, relative string) string {
	if filepath.IsAbs(relative) {
		return relative
	}
	return filepath.Join(dir, relative)
}

func findByHash(entries []Entry, urlHash string) (*Entry, int) {
	for i := range entries {
		if entries[i].URLHash == urlHash {
			return &entries[i], i
		}
	}
	return nil, -1
}

func writeIndex(dir string, entries []Entry) error {
	if err := fsutil.EnsureDir(dir, fsutil.DirMode); err != nil {
		return err
	}
	return fsutil.WriteJSON(filepath.Join(dir, indexFileName), struct {
		Pages []Entry `json:"pages"`
	}{Pages: entries}, fsutil.SecretFileMode)
}

// removeFile 只删 files/ 下的相对路径：索引可能被手工编辑过，
// 绝对路径或 ../ 一律不动。
func removeFile(dir, relative string) {
	if safe, ok := safeRelative(dir, relative); ok {
		_ = os.Remove(safe)
	}
}

func renameFile(dir, from, to string) error {
	src, okSrc := safeRelative(dir, from)
	dst, okDst := safeRelative(dir, to)
	if !okSrc || !okDst {
		return fmt.Errorf("非法的存档路径")
	}
	return os.Rename(src, dst)
}

func safeRelative(dir, relative string) (string, bool) {
	if relative == "" || filepath.IsAbs(relative) {
		return "", false
	}
	resolved := filepath.Join(dir, filepath.FromSlash(relative))
	prefix := filepath.Clean(dir) + string(filepath.Separator)
	if !strings.HasPrefix(resolved, prefix) {
		return "", false
	}
	return resolved, true
}
