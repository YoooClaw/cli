// Package transfer 实现在两个环境之间导出/导入本地数据包（yoooclaw transfer）。
//
// 包格式与 phone-notifications 插件的 `ntf transfer`（src/transfer/package.ts）
// 完全一致（schemaVersion 1）：插件导出的包能导入 CLI，CLI 导出的包也能导入插件。
//
//	<package>/
//	  manifest.json        记录清单（明文，含元数据白名单字段）
//	  blobs/<sha256>       内容寻址的附件（录音音频/转写/摘要、网页正文/存档、图片）
//
// 只迁移通知、录音、网页、图片；不迁移记忆、凭据与宿主配置。
package transfer

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/YoooClaw/cli/internal/version"
)

// 数据类型（顺序即缺省导出顺序）。
const (
	TypeNotifications = "notifications"
	TypeRecordings    = "recordings"
	TypeWebPages      = "web-pages"
	TypeImages        = "images"
)

// Types 是全部支持的数据类型。
var Types = []string{TypeNotifications, TypeRecordings, TypeWebPages, TypeImages}

// 包协议上限。导出端与导入端共用，避免产出一个对端注定拒绝的包。
const (
	MaxRecords       = 100_000
	MaxManifestBytes = 64 * 1024 * 1024
)

// Roots 是各类数据的存储根目录；缺失的类型视为不可用。
type Roots map[string]string

// Asset 是一条记录附带的内容寻址文件。
type Asset struct {
	Field     string `json:"field"`
	Hash      string `json:"hash"`
	Bytes     int64  `json:"bytes"`
	Extension string `json:"extension"`
}

// Record 是包里的一条记录。
type Record struct {
	Type                string         `json:"type"`
	Data                map[string]any `json:"data"`
	Assets              []Asset        `json:"assets"`
	RecordID            string         `json:"recordId"`
	RecordVersion       string         `json:"recordVersion"`
	RecordSchemaVersion int            `json:"recordSchemaVersion"`
	Origin              string         `json:"origin"`
}

// Manifest 是 manifest.json 的内容。
type Manifest struct {
	SchemaVersion       int      `json:"schemaVersion"`
	PackageID           string   `json:"packageId"`
	CreatedAt           string   `json:"createdAt"`
	SourcePluginVersion string   `json:"sourcePluginVersion"`
	Records             []Record `json:"records"`
	Warnings            []string `json:"warnings"`
}

// ExportOptions 是导出范围。
type ExportOptions struct {
	Include    string
	From       string
	To         string
	WithAudio  bool
	WithImages bool
	WithHTML   bool
}

// assetFields 是每类记录允许携带附件的字段。
var assetFields = map[string][]string{
	TypeNotifications: {},
	TypeRecordings:    {"audioFile", "srtFile", "transcriptFile", "transcriptDataFile", "summaryFile"},
	TypeWebPages:      {"relativePath", "archivePath"},
	TypeImages:        {"localFile"},
}

var indexKeys = map[string]string{TypeRecordings: "recordings", TypeWebPages: "pages", TypeImages: "images"}

// 显式的元数据白名单：本地路径、签名 URL、凭据都不能进入包。
var allowedKeys = map[string][]string{
	TypeNotifications: {"clientLabel", "appName", "appDisplayName", "title", "content", "timestamp", "senderName", "conversationType", "conversationName"},
	TypeRecordings:    {"id", "clientLabel", "title", "metadata"},
	TypeWebPages:      {"urlHash", "canonicalUrl", "url", "title", "siteName", "capturedAt", "firstCapturedAt", "captureCount", "contentHash", "clientLabel"},
	TypeImages:        {"imageId", "clientLabel", "metadata"},
}

var metadataKeys = []string{"name", "created_at", "duration_sec", "duration_display", "size_bytes", "mime_type", "width", "height", "source_app", "caption"}

var (
	hashRE      = regexp.MustCompile(`^[a-f0-9]{64}$`)
	extensionRE = regexp.MustCompile(`^\.[a-z0-9]{1,8}$`)
	uuidRE      = regexp.MustCompile(`^[a-f0-9-]{36}$`)
	dayFileRE   = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})\.jsonl?$`)
	zonedTimeRE = regexp.MustCompile(`(Z|[+-]\d\d:\d\d)$`)
)

// Error 是带稳定错误码的迁移错误（码与插件一致，如 PLAN_STALE、CHECKSUM_MISMATCH）。
type Error struct{ Code, Detail string }

func (e *Error) Error() string {
	if e.Detail == "" {
		return e.Code
	}
	return e.Code + ": " + e.Detail
}

func fail(code string) error { return &Error{Code: code} }

func failf(code, format string, args ...any) error {
	return &Error{Code: code, Detail: fmt.Sprintf(format, args...)}
}

// ErrorCode 取出错误码；不是迁移错误时返回空串。
func ErrorCode(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func isScalar(v any) bool {
	switch v.(type) {
	case string, float64, bool:
		return true
	}
	return false
}

func isFiniteNumber(v any) bool {
	f, ok := v.(float64)
	return ok && f-f == 0
}

// normalizeRecordingMetadata 对齐插件 recording/metadata-display.ts 的同名函数：
// 时长取整、按单位重算展示串，只留下规范字段。
func normalizeRecordingMetadata(m map[string]any) map[string]any {
	sec := 0.0
	if f, ok := m["duration_sec"].(float64); ok && f-f == 0 && f > 0 {
		sec = float64(int64(f))
	}
	out := map[string]any{
		"duration_sec":     sec,
		"duration_display": formatDurationDisplay(int64(sec)),
	}
	for _, k := range []string{"name", "created_at", "location", "markers"} {
		if v, ok := m[k]; ok {
			out[k] = v
		}
	}
	return out
}

func formatDurationDisplay(total int64) string {
	h, m, s := total/3600, (total%3600)/60, total%60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh %dm %ds", h, m, s)
	case m > 0:
		return fmt.Sprintf("%dm %ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}

// portable 按白名单提取可迁移字段（对齐插件 portable）。
func portable(typ string, input map[string]any) map[string]any {
	out := map[string]any{}
	if input == nil {
		return out
	}
	if typ == TypeRecordings {
		if meta, ok := input["metadata"].(map[string]any); ok {
			copied := make(map[string]any, len(input))
			for k, v := range input {
				copied[k] = v
			}
			copied["metadata"] = normalizeRecordingMetadata(meta)
			input = copied
		}
	}
	for _, key := range allowedKeys[typ] {
		value, present := input[key]
		if !present {
			continue
		}
		if key != "metadata" {
			if isScalar(value) {
				out[key] = value
			}
			continue
		}
		src, _ := value.(map[string]any)
		meta := map[string]any{}
		for _, k := range metadataKeys {
			if v, ok := src[k]; ok && isScalar(v) {
				meta[k] = v
			}
		}
		if typ == TypeRecordings {
			if markers, ok := src["markers"].([]any); ok {
				kept := []any{}
				for _, raw := range markers {
					m, _ := raw.(map[string]any)
					if isFiniteNumber(m["index"]) && isFiniteNumber(m["timestamp_ms"]) {
						kept = append(kept, map[string]any{"index": m["index"], "timestamp_ms": m["timestamp_ms"]})
					}
				}
				meta["markers"] = kept
			}
			if loc, ok := src["location"].(map[string]any); ok && isFiniteNumber(loc["latitude"]) && isFiniteNumber(loc["longitude"]) {
				meta["location"] = map[string]any{"latitude": loc["latitude"], "longitude": loc["longitude"]}
			}
		}
		out["metadata"] = meta
	}
	return out
}

// identity 是记录在目标端的去重身份：通知取内容哈希（不含 appDisplayName），
// 其余类型取各自的主键。
func identity(typ string, data map[string]any) string {
	if typ == TypeNotifications {
		copied := make(map[string]any, len(data))
		for k, v := range data {
			if k != "appDisplayName" {
				copied[k] = v
			}
		}
		return sha(canonical(portable(typ, copied)))
	}
	for _, k := range []string{"id", "urlHash", "imageId"} {
		if v, ok := data[k]; ok && v != nil {
			return jsString(v)
		}
	}
	return ""
}

var windowsReservedRE = regexp.MustCompile(`(?i)^(?:con|prn|aux|nul|com[1-9]|lpt[1-9])(?:\.|$)`)

// safeStorageID 对齐插件 storage-path.ts 的 storageIdValidationError。
func safeStorageID(v string) error {
	switch {
	case v == "", v == ".", v == "..",
		strings.ContainsAny(v, `/\<>:"|?*`),
		len(v) > 200,
		v != strings.TrimSpace(v),
		strings.HasSuffix(v, "."),
		windowsReservedRE.MatchString(v):
		return fail("INVALID_RECORD_ID")
	}
	for _, r := range v {
		if r < 0x20 || r == 0x7f {
			return fail("INVALID_RECORD_ID")
		}
	}
	return nil
}

func validateData(typ string, d map[string]any) error {
	if d == nil || canonical(portable(typ, d)) != canonical(d) {
		return fail("INVALID_RECORD_DATA")
	}
	id := identity(typ, d)
	if id == "" || utf16Len(id) > 512 {
		return fail("INVALID_RECORD_ID")
	}
	str := func(m map[string]any, k string) (string, bool) {
		s, ok := m[k].(string)
		return s, ok
	}
	switch typ {
	case TypeNotifications:
		for _, k := range []string{"appName", "title", "content", "timestamp"} {
			if _, ok := str(d, k); !ok {
				return fail("INVALID_NOTIFICATION")
			}
		}
		if _, ok := parseTime(d["timestamp"].(string)); !ok {
			return fail("INVALID_NOTIFICATION")
		}
	case TypeRecordings, TypeImages:
		meta, ok := d["metadata"].(map[string]any)
		if !ok {
			return fail("INVALID_METADATA")
		}
		created, ok := str(meta, "created_at")
		if !ok {
			return fail("INVALID_METADATA")
		}
		if _, ok := parseTime(created); !ok {
			return fail("INVALID_METADATA")
		}
		if err := safeStorageID(id); err != nil {
			return err
		}
		if typ == TypeRecordings {
			if _, ok := str(meta, "name"); !ok {
				return fail("INVALID_METADATA")
			}
		}
	case TypeWebPages:
		captured, ok := str(d, "capturedAt")
		if !ok {
			return fail("INVALID_WEB_PAGE")
		}
		if _, ok := parseTime(captured); !ok {
			return fail("INVALID_WEB_PAGE")
		}
		if _, ok := str(d, "firstCapturedAt"); !ok {
			return fail("INVALID_WEB_PAGE")
		}
		_, okURL := str(d, "canonicalUrl")
		_, okTitle := str(d, "title")
		hash, _ := str(d, "urlHash")
		if !okURL || !okTitle || !hashRE.MatchString(hash) {
			return fail("INVALID_WEB_PAGE")
		}
	}
	return nil
}

var zonedLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02T15:04Z07:00",
	"2006-01-02 15:04:05.999999999Z07:00",
	"2006-01-02T15:04:05.999999999Z0700",
	"2006-01-02 15:04:05.999999999Z0700",
	"2006-01-02 15:04:05.999999999 Z07:00",
	time.RFC1123, time.RFC1123Z,
}

var localLayouts = []string{
	"2006-01-02T15:04:05.999999999",
	"2006-01-02T15:04",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02 15:04",
}

// parseTime 近似 JS Date.parse：带时区的 ISO/RFC 时间，无时区按本地时间，
// 纯日期按 UTC（与 ES 规范一致）。
func parseTime(raw string) (time.Time, bool) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return time.Time{}, false
	}
	for _, layout := range zonedLayouts {
		if t, err := time.Parse(layout, value); err == nil {
			return t, true
		}
	}
	for _, layout := range localLayouts {
		if t, err := time.ParseInLocation(layout, value, time.Local); err == nil {
			return t, true
		}
	}
	if t, err := time.Parse("2006-01-02", value); err == nil {
		return t, true
	}
	return time.Time{}, false
}

// safeFile 把包/存储里的相对路径解析成绝对路径，拒绝绝对路径、..、符号链接、
// 多硬链接文件和非普通文件（对齐插件 safeFile）。
func safeFile(root, name string) (string, error) {
	if name == "" || filepath.IsAbs(name) || strings.HasPrefix(name, "/") || strings.Contains(name, `\`) {
		return "", fail("UNSAFE_PATH")
	}
	parts := strings.Split(name, "/")
	for _, p := range parts {
		if p == "" || p == "." || p == ".." {
			return "", fail("UNSAFE_PATH")
		}
	}
	current, err := filepath.Abs(root)
	if err != nil {
		return "", fail("UNSAFE_PATH")
	}
	if info, err := os.Lstat(current); err != nil || info.Mode()&os.ModeSymlink != 0 {
		return "", fail("UNSAFE_PATH")
	}
	var info os.FileInfo
	for _, part := range parts {
		current = filepath.Join(current, part)
		info, err = os.Lstat(current)
		if err != nil {
			return "", fail("UNSAFE_PATH")
		}
		mode := info.Mode()
		if mode&os.ModeSymlink != 0 || (!mode.IsRegular() && !mode.IsDir()) || (mode.IsRegular() && linkCount(info) > 1) {
			return "", fail("UNSAFE_PATH")
		}
	}
	if !info.Mode().IsRegular() {
		return "", fail("INVALID_FILE")
	}
	return current, nil
}

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func readJSONFile(path string) (any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// entries 读取某类数据的已落盘记录（原始 JSON 对象）。
//
// 传了 onWarn 时，单个无法解析的文件只跳过并报告，不让整次导出/预览失败——
// 存储层本身对损坏日文件就是容错的，这里保持同样的宽容度。
//
// 通知按 CLI 的日文件格式读取：YYYY-MM-DD.jsonl 优先（坏行跳过），缺失时回退
// 旧的 YYYY-MM-DD.json 数组（也是插件的格式）。
func entries(root, typ string, onWarn func(string)) ([]map[string]any, error) {
	warnOrFail := func(name string, err error) error {
		if onWarn == nil {
			return err
		}
		onWarn(fmt.Sprintf("%s/%s: unreadable, skipped (%s)", typ, name, err.Error()))
		return nil
	}
	objects := func(name string, list []any) []map[string]any {
		out := make([]map[string]any, 0, len(list))
		skipped := 0
		for _, item := range list {
			if m, ok := item.(map[string]any); ok {
				out = append(out, m)
			} else {
				skipped++
			}
		}
		if skipped > 0 && onWarn != nil {
			onWarn(fmt.Sprintf("%s/%s: %d invalid entries skipped", typ, name, skipped))
		}
		return out
	}
	if typ == TypeNotifications {
		dirEntries, err := os.ReadDir(root)
		if err != nil {
			return nil, err
		}
		files := map[string][]string{}
		var days []string
		for _, e := range dirEntries {
			m := dayFileRE.FindStringSubmatch(e.Name())
			if m == nil || e.IsDir() {
				continue
			}
			if _, seen := files[m[1]]; !seen {
				days = append(days, m[1])
			}
			files[m[1]] = append(files[m[1]], e.Name())
		}
		sort.Strings(days)
		var out []map[string]any
		for _, day := range days {
			name := day + ".json"
			for _, candidate := range files[day] {
				if strings.HasSuffix(candidate, ".jsonl") {
					name = candidate
				}
			}
			path, err := safeFile(root, name)
			if err != nil {
				if werr := warnOrFail(name, err); werr != nil {
					return nil, werr
				}
				continue
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				if werr := warnOrFail(name, err); werr != nil {
					return nil, werr
				}
				continue
			}
			if strings.HasSuffix(name, ".jsonl") {
				var list []any
				bad := 0
				for _, line := range bytes.Split(raw, []byte{'\n'}) {
					line = bytes.TrimSpace(line)
					if len(line) == 0 {
						continue
					}
					var item any
					if json.Unmarshal(line, &item) != nil {
						bad++
						continue
					}
					list = append(list, item)
				}
				if bad > 0 && onWarn != nil {
					onWarn(fmt.Sprintf("%s/%s: %d unreadable lines skipped", typ, name, bad))
				}
				out = append(out, objects(name, list)...)
				continue
			}
			var list []any
			if len(bytes.TrimSpace(raw)) > 0 {
				if err := json.Unmarshal(raw, &list); err != nil {
					if werr := warnOrFail(name, fail("INVALID_SOURCE")); werr != nil {
						return nil, werr
					}
					continue
				}
			}
			out = append(out, objects(name, list)...)
		}
		return out, nil
	}
	if _, err := os.Lstat(filepath.Join(root, "index.json")); err != nil {
		return nil, nil
	}
	path, err := safeFile(root, "index.json")
	if err != nil {
		return nil, warnOrFail("index.json", err)
	}
	parsed, err := readJSONFile(path)
	if err != nil {
		return nil, warnOrFail("index.json", err)
	}
	obj, _ := parsed.(map[string]any)
	list, ok := obj[indexKeys[typ]].([]any)
	if !ok {
		return nil, warnOrFail("index.json", fail("INVALID_SOURCE"))
	}
	return objects("index.json", list), nil
}

func selectedFields(typ string, opts ExportOptions) []string {
	var out []string
	for _, f := range assetFields[typ] {
		if (f == "audioFile" && !opts.WithAudio) || (f == "archivePath" && !opts.WithHTML) {
			continue
		}
		out = append(out, f)
	}
	return out
}

func recordVersion(data map[string]any, assets []Asset) string {
	pairs := make([]any, 0, len(assets))
	sorted := append([]Asset(nil), assets...)
	sort.SliceStable(sorted, func(i, j int) bool { return strings.ToLower(sorted[i].Field) < strings.ToLower(sorted[j].Field) })
	for _, a := range sorted {
		pairs = append(pairs, map[string]any{"field": a.Field, "hash": a.Hash})
	}
	return sha(canonical(map[string]any{"data": data, "assets": pairs}))
}

// sourceOrigin 返回源作用域的稳定身份，用于多跳迁移保留原始来源。
//
// 标记文件落在数据目录内：它必须跟着数据走。这是导出**唯一**会写源目录的
// 地方，且只在真正产包时写（--dry-run 不写）。
func sourceOrigin(root string, persist bool) (string, error) {
	path := filepath.Join(root, ".transfer-origin")
	if _, err := os.Lstat(path); err == nil {
		file, err := safeFile(root, ".transfer-origin")
		if err != nil {
			return "", err
		}
		raw, err := os.ReadFile(file)
		if err != nil {
			return "", err
		}
		value := strings.TrimSpace(string(raw))
		if !uuidRE.MatchString(value) {
			return "", fail("INVALID_SOURCE_ORIGIN")
		}
		return value, nil
	}
	value := newUUID()
	if persist {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			if os.IsExist(err) {
				return sourceOrigin(root, false)
			}
			return "", err
		}
		_, werr := f.WriteString(value)
		cerr := f.Close()
		if werr != nil {
			return "", werr
		}
		if cerr != nil {
			return "", cerr
		}
	}
	return value, nil
}

func isInside(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel))
}

func timeBound(v string) (*time.Time, error) {
	if v == "" {
		return nil, nil
	}
	if !zonedTimeRE.MatchString(v) {
		return nil, fail("INVALID_TIME")
	}
	t, ok := parseTime(v)
	if !ok {
		return nil, fail("INVALID_TIME")
	}
	return &t, nil
}

// recordTime 取记录用于时间过滤的时间戳（通知 timestamp / 元数据 created_at / 网页 capturedAt）。
func recordTime(data map[string]any) (time.Time, bool) {
	if s, ok := data["timestamp"].(string); ok {
		return parseTime(s)
	}
	if meta, ok := data["metadata"].(map[string]any); ok {
		if s, ok := meta["created_at"].(string); ok {
			return parseTime(s)
		}
	}
	if s, ok := data["capturedAt"].(string); ok {
		return parseTime(s)
	}
	return time.Time{}, false
}

func stringField(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

// Export 生成迁移包；output 为空时只预览（dry-run），不写任何文件。
func Export(roots Roots, output string, opts ExportOptions) (*Manifest, error) {
	include := opts.Include
	if include == "" {
		include = "notifications,recordings,web-pages"
	}
	types := strings.Split(include, ",")
	if opts.WithImages && !contains(types, TypeImages) {
		types = append(types, TypeImages)
	}
	for _, t := range types {
		if !contains(Types, t) {
			return nil, fail("INVALID_SCOPE")
		}
	}
	from, err := timeBound(opts.From)
	if err != nil {
		return nil, err
	}
	to, err := timeBound(opts.To)
	if err != nil {
		return nil, err
	}
	if from != nil && to != nil && !from.Before(*to) {
		return nil, fail("INVALID_TIME_RANGE")
	}
	if output != "" {
		if _, err := os.Lstat(output); err == nil {
			return nil, fail("OUTPUT_EXISTS")
		}
		for _, root := range roots {
			if root == "" {
				continue
			}
			absRoot, _ := filepath.Abs(root)
			if isInside(absRoot, output) {
				return nil, fail("OUTPUT_INSIDE_SOURCE")
			}
		}
		if err := os.MkdirAll(filepath.Join(output, "blobs"), 0o700); err != nil {
			return nil, err
		}
	}
	m := &Manifest{
		SchemaVersion: 1, PackageID: newUUID(), CreatedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		SourcePluginVersion: version.Version, Records: []Record{}, Warnings: []string{},
	}
	warn := func(s string) { m.Warnings = append(m.Warnings, s) }
	seenTypes := map[string]bool{}
	for _, typ := range types {
		if seenTypes[typ] {
			continue
		}
		seenTypes[typ] = true
		root := roots[typ]
		if root == "" || !dirExists(root) {
			warn(typ + ": unavailable")
			continue
		}
		origin, err := sourceOrigin(root, output != "")
		if err != nil {
			return nil, err
		}
		raws, err := entries(root, typ, warn)
		if err != nil {
			return nil, err
		}
		for _, raw := range raws {
			data := portable(typ, raw)
			if from != nil || to != nil {
				ts, ok := recordTime(data)
				if !ok {
					warn(typ + ": missing timestamp")
					continue
				}
				if (from != nil && ts.Before(*from)) || (to != nil && !ts.Before(*to)) {
					continue
				}
			}
			record, err := exportRecord(root, typ, raw, data, origin, output, opts, warn)
			if err != nil {
				warn(fmt.Sprintf("%s: skipped invalid record (%s)", typ, err.Error()))
				continue
			}
			m.Records = append(m.Records, *record)
		}
	}
	m.Records = dedupeRecords(m.Records)
	// 导出端提前判上限，否则用户搬完整个目录才会在对端撞上拒绝。
	if len(m.Records) > MaxRecords {
		return nil, fail("PACKAGE_TOO_MANY_RECORDS")
	}
	serialized, err := marshalManifest(m)
	if err != nil {
		return nil, err
	}
	if len(serialized) > MaxManifestBytes {
		return nil, fail("MANIFEST_TOO_LARGE")
	}
	if output != "" {
		if err := os.WriteFile(filepath.Join(output, "manifest.json"), serialized, 0o600); err != nil {
			return nil, err
		}
	}
	return m, nil
}

func exportRecord(root, typ string, raw, data map[string]any, origin, output string, opts ExportOptions, warn func(string)) (*Record, error) {
	if err := validateData(typ, data); err != nil {
		return nil, err
	}
	assets := []Asset{}
	for _, field := range selectedFields(typ, opts) {
		rel, _ := raw[field].(string)
		if rel == "" {
			continue
		}
		asset, err := exportAsset(root, rel, output)
		if err != nil {
			warn(fmt.Sprintf("%s/%s: missing or changing %s", typ, identity(typ, data), field))
			continue
		}
		asset.Field = field
		assets = append(assets, *asset)
	}
	if typ == TypeWebPages && !hasField(assets, "relativePath") {
		return nil, fail("MISSING_BODY")
	}
	if tr, ok := raw["transfer"].(map[string]any); ok {
		if o, ok := tr["origin"].(string); ok {
			origin = o
		}
	}
	return &Record{
		Type: typ, Data: data, Assets: assets, RecordID: identity(typ, data),
		RecordVersion: recordVersion(data, assets), RecordSchemaVersion: 1, Origin: origin,
	}, nil
}

func exportAsset(root, rel, output string) (*Asset, error) {
	file, err := safeFile(root, rel)
	if err != nil {
		return nil, err
	}
	before, err := os.Stat(file)
	if err != nil {
		return nil, err
	}
	source := file
	var tmp string
	if output != "" {
		tmp = filepath.Join(output, "blobs", newUUID())
		if err := copyFile(file, tmp); err != nil {
			_ = os.Remove(tmp)
			return nil, err
		}
		source = tmp
	}
	hash, err := hashFile(source)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(source)
	if err != nil {
		return nil, err
	}
	after, err := os.Stat(file)
	if err != nil || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		if tmp != "" {
			_ = os.Remove(tmp)
		}
		return nil, fail("SOURCE_CHANGED")
	}
	if tmp != "" {
		dest := filepath.Join(output, "blobs", hash)
		if _, err := os.Lstat(dest); err == nil {
			_ = os.Remove(tmp)
		} else if err := os.Rename(tmp, dest); err != nil {
			return nil, err
		}
	}
	ext := strings.ToLower(filepath.Ext(file))
	if !extensionRE.MatchString(ext) {
		ext = ".bin"
	}
	return &Asset{Hash: hash, Bytes: info.Size(), Extension: ext}, nil
}

// dedupeRecords 按 type:recordId 去重：位置取首次出现、内容取最后一次（对齐 JS Map 语义）。
func dedupeRecords(records []Record) []Record {
	pos := map[string]int{}
	out := make([]Record, 0, len(records))
	for _, r := range records {
		key := r.Type + ":" + r.RecordID
		if i, ok := pos[key]; ok {
			out[i] = r
			continue
		}
		pos[key] = len(out)
		out = append(out, r)
	}
	return out
}

func marshalManifest(m *Manifest) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(m); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// ReadOptions 控制包校验强度。
type ReadOptions struct {
	// SkipBlobHash 只保留结构、身份与体积校验，跳过逐字节重算——仅用于本机
	// 0700 staging 目录里已经全量校验过的副本。外部来源的包必须全量校验。
	SkipBlobHash bool
}

// ReadPackage 校验并读取一个包。
func ReadPackage(root string, opts ReadOptions) (*Manifest, error) {
	mf, err := safeFile(root, "manifest.json")
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(mf)
	if err != nil {
		return nil, err
	}
	if info.Size() > MaxManifestBytes {
		return nil, fail("MANIFEST_TOO_LARGE")
	}
	raw, err := os.ReadFile(mf)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, failf("VERSION_UNSUPPORTED", "invalid manifest: %s", err.Error())
	}
	if m.SchemaVersion != 1 || m.Records == nil || len(m.Records) > MaxRecords || m.Warnings == nil || m.PackageID == "" {
		return nil, fail("VERSION_UNSUPPORTED")
	}
	ids := map[string]bool{}
	for _, r := range m.Records {
		if !contains(Types, r.Type) || r.RecordSchemaVersion != 1 || r.Assets == nil {
			return nil, fail("VERSION_UNSUPPORTED")
		}
		if err := validateData(r.Type, r.Data); err != nil {
			return nil, err
		}
		if r.RecordID != identity(r.Type, r.Data) || r.RecordVersion != recordVersion(r.Data, r.Assets) {
			return nil, fail("CHECKSUM_MISMATCH")
		}
		id := r.Type + ":" + r.RecordID
		if ids[id] {
			return nil, fail("DUPLICATE_RECORD")
		}
		ids[id] = true
		seen := map[string]bool{}
		for _, a := range r.Assets {
			if !contains(assetFields[r.Type], a.Field) || seen[a.Field] || !hashRE.MatchString(a.Hash) || !extensionRE.MatchString(a.Extension) {
				return nil, fail("INVALID_ASSET")
			}
			seen[a.Field] = true
			p, err := safeFile(root, "blobs/"+a.Hash)
			if err != nil {
				return nil, err
			}
			st, err := os.Stat(p)
			if err != nil || st.Size() != a.Bytes {
				return nil, fail("CHECKSUM_MISMATCH")
			}
			if !opts.SkipBlobHash {
				if h, err := hashFile(p); err != nil || h != a.Hash {
					return nil, fail("CHECKSUM_MISMATCH")
				}
			}
		}
		if r.Type == TypeWebPages && !seen["relativePath"] {
			return nil, fail("MISSING_BODY")
		}
	}
	return &m, nil
}

// Capabilities 描述本端支持的包协议（与插件 capabilities() 同构，互相可校验）。
func Capabilities() map[string]any {
	return map[string]any{
		"pluginVersion":                   version.Version,
		"host":                            "yoooclaw-cli",
		"supportedSchemaVersions":         []int{1},
		"supportedRecordVersions":         []int{1},
		"supportedDataTypes":              Types,
		"notificationImportPolicyVersion": 1,
		"transport":                       []string{"local"},
	}
}

// CheckCapabilities 校验目标端能力文件（插件或 CLI 生成的都可以）。
func CheckCapabilities(c map[string]any) error {
	hasOne := func(list any) bool {
		items, _ := list.([]any)
		for _, v := range items {
			if f, ok := v.(float64); ok && f == 1 {
				return true
			}
		}
		return false
	}
	if c == nil || !hasOne(c["supportedSchemaVersions"]) {
		return fail("VERSION_UNSUPPORTED")
	}
	if v, _ := c["notificationImportPolicyVersion"].(float64); v != 1 {
		return fail("VERSION_UNSUPPORTED")
	}
	types, _ := c["supportedDataTypes"].([]any)
	for _, t := range Types {
		found := false
		for _, v := range types {
			if v == t {
				found = true
			}
		}
		if !found {
			return fail("VERSION_UNSUPPORTED")
		}
	}
	return nil
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

func hasField(assets []Asset, field string) bool {
	for _, a := range assets {
		if a.Field == field {
			return true
		}
	}
	return false
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := out.ReadFrom(in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
