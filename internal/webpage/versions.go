package webpage

// 同一 URL 的多版本快照（yc-web-extension/docs/web-page-versions-prd.html）。
//
//	<profile>/web-pages/
//	├── files/<hash8>-<slug>.md          # 永远是最新版，布局与单版本时代一字不差
//	└── versions/<hash8>/
//	    ├── chain.json                   # 版本链：从 frontmatter 派生，可随时重算
//	    ├── v000001.md
//	    └── v000002.md
//
// 真相在每份正文的 frontmatter 里（prev/next 指针、观察次数、bodyHash），
// chain.json 只是把它们汇成一份读起来方便的视图——所以这里每次都从磁盘重算，
// 而不是增量维护一份可能与文件对不上的副本。

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/YoooClaw/cli/internal/fsutil"
)

const (
	versionsDirName = "versions"
	chainFileName   = "chain.json"
	chainSchema     = 1

	// MergeWindow 与 MergeRatio 是 R4「补录」的两道门：离上一版不到 10 分钟、
	// 变化又不到 2%，就地替换最新版而不新增版本号（PRD §3.1，数值待 Q-V3 校准）。
	MergeWindow = 10 * time.Minute
	MergeRatio  = 0.02

	// TrackingAuto 是缺省：第二次收藏同一 URL 时自动开始记版本（§3.2）。
	TrackingAuto = "auto"
	TrackingOn   = "on"
	TrackingOff  = "off"

	sourceManual      = "manual"
	notePreVersioning = "pre-versioning"
)

// Change 是相邻两版的变化量，落在 frontmatter 与 chain.json 里，
// 让 agent 不必为了回答「变了吗」去全文 diff。
type Change struct {
	Ratio   float64 `json:"ratio"`
	Added   int     `json:"added"`
	Removed int     `json:"removed"`
}

// versionMeta 是一份版本正文 frontmatter 里的版本字段。
type versionMeta struct {
	Tracking         string // 只写在最新版上
	Version          int
	Prev, Next       int // 0 = 没有
	BodyHash         string
	ObservationCount int
	FirstSeenAt      string
	LastSeenAt       string
	Changed          *Change
	Source           string
	Late             bool
	Note             string
}

// versionKeys 是版本字段在 frontmatter 里的固定顺序；重写时先整体删掉再按序追加。
var versionKeys = []string{
	"tracking", "version", "versionOf", "prevVersion", "prevPath", "nextVersion", "nextPath",
	"bodyHash", "observationCount", "firstSeenAt", "lastSeenAt", "changedFromPrev",
	"source", "late", "note",
}

// ValidTracking 判断追踪开关取值是否合法。
func ValidTracking(value string) bool {
	return value == TrackingAuto || value == TrackingOn || value == TrackingOff
}

func trackingOf(entry *Entry) string {
	if entry != nil && ValidTracking(entry.Tracking) {
		return entry.Tracking
	}
	return TrackingAuto
}

func versionRel(hash8 string, version int) string {
	return fmt.Sprintf("%s/%s/v%06d.md", versionsDirName, hash8, version)
}

func chainRel(hash8 string) string {
	return versionsDirName + "/" + hash8 + "/" + chainFileName
}

// relFrom 把 web-pages 根下的相对路径 target 改写成相对 self 所在目录的路径，
// 让任何一份版本文件单独拿出来都能顺着指针走到邻居。
func relFrom(self, target string) string {
	rel, err := filepath.Rel(filepath.FromSlash(path.Dir(self)), filepath.FromSlash(target))
	if err != nil {
		return target
	}
	return filepath.ToSlash(rel)
}

// ── 正文规范化与变化量 ──────────────────────────────────────────

// normalizedLines 是 bodyHash 与变化率共用的输入：折叠行内空白、去掉空行与纯装饰行。
// 只动排版不动语义——「3 分钟前」这类相对时间留给变化率阈值去吸收（Q-V6）。
func normalizedLines(body string) []string {
	var lines []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.Join(strings.Fields(line), " ")
		if line == "" || isDecorativeLine(line) {
			continue
		}
		lines = append(lines, line)
	}
	return lines
}

func isDecorativeLine(line string) bool {
	for _, r := range line {
		if !strings.ContainsRune("-_*=~#>|:+.` ", r) {
			return false
		}
	}
	return true
}

// BodyHash 是规范化正文的 sha256 前 16 位，与扩展的 contentHash 同长。
func BodyHash(body string) string {
	sum := sha256.Sum256([]byte(strings.Join(normalizedLines(body), "\n")))
	return hex.EncodeToString(sum[:])[:16]
}

// Diff 按规范化行的多重集合计增删：不看顺序，所以数据页调换行序不算变化，
// 三端也不必各自实现一遍 LCS。ratio 保留 4 位小数，让三端序列化结果一致。
func Diff(before, after string) Change {
	oldLines, newLines := normalizedLines(before), normalizedLines(after)
	pool := map[string]int{}
	for _, line := range oldLines {
		pool[line]++
	}
	added := 0
	for _, line := range newLines {
		if pool[line] > 0 {
			pool[line]--
		} else {
			added++
		}
	}
	removed := 0
	for _, n := range pool {
		removed += n
	}
	total := len(oldLines) + len(newLines)
	if total == 0 {
		return Change{}
	}
	ratio := math.Round(float64(added+removed)/float64(total)*10000) / 10000
	return Change{Ratio: ratio, Added: added, Removed: removed}
}

// ── frontmatter 读写 ──────────────────────────────────────────

// splitDocument 把 BuildDocument 产物拆成 frontmatter 行与其后的全部内容。
func splitDocument(doc string) ([]string, string, bool) {
	if !strings.HasPrefix(doc, "---\n") {
		return nil, "", false
	}
	rest := doc[len("---\n"):]
	end := strings.Index(rest, "\n---\n")
	if end < 0 {
		return nil, "", false
	}
	return strings.Split(rest[:end], "\n"), rest[end+len("\n---\n"):], true
}

func joinDocument(front []string, body string) string {
	return "---\n" + strings.Join(front, "\n") + "\n---\n" + body
}

func frontRaw(front []string, key string) (string, bool) {
	prefix := key + ": "
	for _, line := range front {
		if strings.HasPrefix(line, prefix) {
			return line[len(prefix):], true
		}
	}
	return "", false
}

// frontString 读一个字符串值。writeYAMLString 的转义是 JSON 的子集，直接按 JSON 解。
func frontString(front []string, key string) string {
	raw, ok := frontRaw(front, key)
	if !ok {
		return ""
	}
	var value string
	if json.Unmarshal([]byte(raw), &value) != nil {
		return ""
	}
	return value
}

func frontInt(front []string, key string) int {
	raw, _ := frontRaw(front, key)
	n, _ := strconv.Atoi(raw)
	return n
}

func withoutKeys(front []string, keys ...string) []string {
	drop := map[string]bool{}
	for _, key := range keys {
		drop[key] = true
	}
	kept := make([]string, 0, len(front))
	for _, line := range front {
		key, _, _ := strings.Cut(line, ": ")
		if !drop[key] {
			kept = append(kept, line)
		}
	}
	return kept
}

// withFrontString 设置（空值则删除）一个字符串字段，位置保持在版本字段之前。
func withFrontString(front []string, key, value string) []string {
	for i, line := range front {
		if strings.HasPrefix(line, key+": ") {
			if value == "" {
				return append(append([]string{}, front[:i]...), front[i+1:]...)
			}
			out := append([]string{}, front...)
			out[i] = key + ": " + quoteYAML(value)
			return out
		}
	}
	if value == "" {
		return front
	}
	at := len(front)
	for i, line := range front {
		name, _, _ := strings.Cut(line, ": ")
		if isVersionKey(name) {
			at = i
			break
		}
	}
	out := append(append([]string{}, front[:at]...), key+": "+quoteYAML(value))
	return append(out, front[at:]...)
}

func isVersionKey(key string) bool {
	for _, candidate := range versionKeys {
		if candidate == key {
			return true
		}
	}
	return false
}

func parseMeta(front []string) (versionMeta, bool) {
	version := frontInt(front, "version")
	if version <= 0 {
		return versionMeta{}, false
	}
	meta := versionMeta{
		Tracking:         frontString(front, "tracking"),
		Version:          version,
		Prev:             frontInt(front, "prevVersion"),
		Next:             frontInt(front, "nextVersion"),
		BodyHash:         frontString(front, "bodyHash"),
		ObservationCount: frontInt(front, "observationCount"),
		FirstSeenAt:      frontString(front, "firstSeenAt"),
		LastSeenAt:       frontString(front, "lastSeenAt"),
		Source:           frontString(front, "source"),
		Note:             frontString(front, "note"),
	}
	if raw, ok := frontRaw(front, "late"); ok {
		meta.Late = raw == "true"
	}
	if raw, ok := frontRaw(front, "changedFromPrev"); ok && raw != "null" {
		var change Change
		if json.Unmarshal([]byte(raw), &change) == nil {
			meta.Changed = &change
		}
	}
	return meta, true
}

// linkTarget 给出版本 v 在 web-pages 根下的相对路径：最新版在 files/，其余在 versions/。
type linkTarget struct {
	hash8         string
	latestVersion int
	latestRel     string
}

func (l linkTarget) pathOf(version int) string {
	if version == l.latestVersion {
		return l.latestRel
	}
	return versionRel(l.hash8, version)
}

// renderMeta 把版本字段写回 frontmatter。prev/next 路径相对文件自身所在目录，
// 所以同一份 meta 放在 files/ 与 versions/ 下渲染出的路径不同——每次写都现算。
func renderMeta(front []string, meta versionMeta, self string, links linkTarget) []string {
	out := withoutKeys(front, versionKeys...)
	add := func(key, value string) { out = append(out, key+": "+value) }
	pointer := func(numKey, pathKey string, version int) {
		if version <= 0 {
			add(numKey, "null")
			add(pathKey, "null")
			return
		}
		add(numKey, strconv.Itoa(version))
		add(pathKey, quoteYAML(relFrom(self, links.pathOf(version))))
	}
	if meta.Tracking != "" {
		add("tracking", quoteYAML(meta.Tracking))
	}
	add("version", strconv.Itoa(meta.Version))
	add("versionOf", quoteYAML(links.hash8))
	pointer("prevVersion", "prevPath", meta.Prev)
	pointer("nextVersion", "nextPath", meta.Next)
	add("bodyHash", quoteYAML(meta.BodyHash))
	add("observationCount", strconv.Itoa(meta.ObservationCount))
	add("firstSeenAt", quoteYAML(meta.FirstSeenAt))
	add("lastSeenAt", quoteYAML(meta.LastSeenAt))
	if meta.Changed == nil {
		add("changedFromPrev", "null")
	} else {
		raw, _ := json.Marshal(meta.Changed)
		add("changedFromPrev", string(raw))
	}
	add("source", quoteYAML(meta.Source))
	if meta.Late {
		add("late", "true")
	}
	if meta.Note != "" {
		add("note", quoteYAML(meta.Note))
	}
	return out
}

// ── 时间 ──────────────────────────────────────────

func parseTime(value string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339, value)
	return t, err == nil
}

// before 只在两个时间都能解析时比较；解析不了一律视为「不早于」，
// 宁可少打一个 late 标记，也不因一个坏时间戳乱排趋势。
func before(a, b string) bool {
	ta, okA := parseTime(a)
	tb, okB := parseTime(b)
	return okA && okB && ta.Before(tb)
}

func laterOf(a, b string) string {
	if a == "" || before(a, b) {
		return b
	}
	return a
}

func withinMergeWindow(latest, incoming string) bool {
	tl, okL := parseTime(latest)
	ti, okI := parseTime(incoming)
	if !okL || !okI {
		return false
	}
	gap := ti.Sub(tl)
	return gap >= 0 && gap < MergeWindow
}

// ── chain.json ──────────────────────────────────────────

// Chain 是 versions/<hash8>/chain.json。字段顺序即落盘顺序，三端需逐字节一致。
type Chain struct {
	Schema           int            `json:"schema"`
	URLHash          string         `json:"urlHash"`
	CanonicalURL     string         `json:"canonicalUrl"`
	Title            string         `json:"title"`
	SiteName         string         `json:"siteName,omitempty"`
	Tracking         string         `json:"tracking"`
	FirstCapturedAt  string         `json:"firstCapturedAt"`
	LatestCapturedAt string         `json:"latestCapturedAt"`
	LastChangedAt    string         `json:"lastChangedAt"`
	LatestVersion    int            `json:"latestVersion"`
	VersionCount     int            `json:"versionCount"`
	ObservationCount int            `json:"observationCount"`
	Versions         []ChainVersion `json:"versions"`
	Evicted          []any          `json:"evicted"`
}

// ChainVersion 是链上一条版本记录。path 相对 chain.json 所在目录。
type ChainVersion struct {
	Version          int     `json:"version"`
	Path             string  `json:"path"`
	CapturedAt       string  `json:"capturedAt"`
	FirstSeenAt      string  `json:"firstSeenAt"`
	LastSeenAt       string  `json:"lastSeenAt"`
	ObservationCount int     `json:"observationCount"`
	ContentHash      string  `json:"contentHash,omitempty"`
	BodyHash         string  `json:"bodyHash"`
	Bytes            int     `json:"bytes"`
	Source           string  `json:"source"`
	Late             bool    `json:"late,omitempty"`
	Note             string  `json:"note,omitempty"`
	Prev             *int    `json:"prev"`
	Next             *int    `json:"next"`
	ChangedFromPrev  *Change `json:"changedFromPrev"`
}

func intPtr(n int) *int {
	if n <= 0 {
		return nil
	}
	return &n
}

// BuildChain 从 frontmatter 重算一条链：最新版读 latestRel，历史版扫 versions/<hash8>/v*.md。
func BuildChain(dir, urlHash, latestRel string) (Chain, error) {
	hash8 := urlHash[:8]
	latestDoc, err := readRelative(dir, latestRel)
	if err != nil {
		return Chain{}, err
	}
	latestFront, _, ok := splitDocument(latestDoc)
	if !ok {
		return Chain{}, fmt.Errorf("最新版缺少 frontmatter")
	}
	latestMeta, ok := parseMeta(latestFront)
	if !ok {
		return Chain{}, fmt.Errorf("最新版缺少版本字段")
	}

	chain := Chain{
		Schema:       chainSchema,
		URLHash:      urlHash,
		CanonicalURL: frontString(latestFront, "canonicalUrl"),
		Title:        frontString(latestFront, "title"),
		SiteName:     frontString(latestFront, "siteName"),
		Tracking:     latestMeta.Tracking,
		Evicted:      []any{},
	}
	if chain.Tracking == "" {
		chain.Tracking = TrackingAuto
	}

	add := func(front []string, meta versionMeta, chainPath string, bytes int) {
		chain.Versions = append(chain.Versions, ChainVersion{
			Version:          meta.Version,
			Path:             chainPath,
			CapturedAt:       frontString(front, "capturedAt"),
			FirstSeenAt:      meta.FirstSeenAt,
			LastSeenAt:       meta.LastSeenAt,
			ObservationCount: meta.ObservationCount,
			ContentHash:      frontString(front, "contentHash"),
			BodyHash:         meta.BodyHash,
			Bytes:            bytes,
			Source:           meta.Source,
			Late:             meta.Late,
			Note:             meta.Note,
			Prev:             intPtr(meta.Prev),
			Next:             intPtr(meta.Next),
			ChangedFromPrev:  meta.Changed,
		})
	}
	add(latestFront, latestMeta, "../../"+latestRel, len(latestDoc))

	historyDir := filepath.Join(dir, versionsDirName, hash8)
	names, _ := os.ReadDir(historyDir)
	for _, item := range names {
		name := item.Name()
		if item.IsDir() || !strings.HasPrefix(name, "v") || !strings.HasSuffix(name, ".md") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(historyDir, name))
		if err != nil {
			continue
		}
		front, _, ok := splitDocument(string(raw))
		if !ok {
			continue
		}
		meta, ok := parseMeta(front)
		// 与最新版同号的历史文件只可能是中断留下的残片（先写历史副本、再换最新版），跳过。
		if !ok || meta.Version >= latestMeta.Version {
			continue
		}
		add(front, meta, name, len(raw))
	}

	// 降序：读的人十有八九只要最近几版（§4.3）。
	sort.Slice(chain.Versions, func(i, j int) bool {
		return chain.Versions[i].Version > chain.Versions[j].Version
	})
	chain.LatestVersion = latestMeta.Version
	chain.VersionCount = len(chain.Versions)
	for _, v := range chain.Versions {
		chain.ObservationCount += v.ObservationCount
		if chain.FirstCapturedAt == "" || before(v.FirstSeenAt, chain.FirstCapturedAt) {
			chain.FirstCapturedAt = v.FirstSeenAt
		}
		chain.LatestCapturedAt = laterOf(chain.LatestCapturedAt, v.LastSeenAt)
		chain.LastChangedAt = laterOf(chain.LastChangedAt, v.FirstSeenAt)
	}
	return chain, nil
}

// ReadChain 读 versions/<hash8>/chain.json；不存在或损坏返回 false。
func ReadChain(dir, urlHash string) (Chain, bool) {
	var chain Chain
	if len(urlHash) < 8 {
		return chain, false
	}
	raw, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(chainRel(urlHash[:8]))))
	if err != nil || json.Unmarshal(raw, &chain) != nil {
		return chain, false
	}
	return chain, true
}

func writeChain(dir string, chain Chain) error {
	target := filepath.Join(dir, filepath.FromSlash(chainRel(chain.URLHash[:8])))
	if err := fsutil.EnsureDir(filepath.Dir(target), fsutil.DirMode); err != nil {
		return err
	}
	return fsutil.WriteJSON(target, chain, fsutil.SecretFileMode)
}

func readRelative(dir, relative string) (string, error) {
	safe, ok := safeRelative(dir, relative)
	if !ok {
		return "", fmt.Errorf("非法的相对路径 %q", relative)
	}
	raw, err := os.ReadFile(safe)
	return string(raw), err
}

func writeRelative(dir, relative, content string) error {
	safe, ok := safeRelative(dir, relative)
	if !ok {
		return fmt.Errorf("非法的相对路径 %q", relative)
	}
	if err := fsutil.EnsureDir(filepath.Dir(safe), fsutil.DirMode); err != nil {
		return err
	}
	return fsutil.WriteAtomic(safe, []byte(content), fsutil.SecretFileMode)
}
