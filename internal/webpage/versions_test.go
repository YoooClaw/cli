package webpage

import (
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
)

// capture 生成一次「内容不同」的收藏：正文足够长，改一行也会越过 2% 的补录阈值。
func capture(at, marker string) Payload {
	payload := samplePayload()
	payload.CapturedAt = at
	payload.ContentHash = "hash-" + marker
	var body strings.Builder
	body.WriteString("| 指标 | 值 |\n| --- | --- |\n")
	body.WriteString("| 本月营收 | " + marker + " |\n")
	for i := 0; i < 10; i++ {
		body.WriteString("第 " + string(rune('A'+i)) + " 段固定内容。\n\n")
	}
	payload.Markdown = body.String()
	return payload
}

func mustIngest(t *testing.T, dir string, payload Payload) IngestResult {
	t.Helper()
	result, err := Ingest(dir, payload, "webext", &testLogger{})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	return result
}

func readFront(t *testing.T, dir, rel string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("读取 %s: %v", rel, err)
	}
	front, _, ok := splitDocument(string(raw))
	if !ok {
		t.Fatalf("%s 没有 frontmatter", rel)
	}
	return front
}

// follow 从 rel 出发沿 key 指针走一步，返回新的根相对路径；指针为 null 时返回空。
func follow(t *testing.T, dir, rel, key string) string {
	t.Helper()
	target := frontString(readFront(t, dir, rel), key)
	if target == "" {
		return ""
	}
	return path.Clean(path.Join(path.Dir(rel), target))
}

func TestVersionsThreeCapturesCrossLink(t *testing.T) {
	dir := t.TempDir()
	first := mustIngest(t, dir, capture("2026-08-01T09:00:00+08:00", "100"))
	if first.Version != 1 || first.VersionCount != 1 || first.Tracking != TrackingAuto {
		t.Fatalf("首次收藏回执不对: %+v", first)
	}
	if _, err := os.Stat(filepath.Join(dir, versionsDirName)); !os.IsNotExist(err) {
		t.Fatal("首次收藏不应建版本目录")
	}

	second := mustIngest(t, dir, capture("2026-08-02T09:00:00+08:00", "120"))
	third := mustIngest(t, dir, capture("2026-08-03T09:00:00+08:00", "150"))
	if third.Version != 3 || third.VersionCount != 3 || !*third.VersionCreated || *third.Unchanged {
		t.Fatalf("第三次收藏回执不对: %+v", third)
	}
	if second.ChangedRatio == nil || *second.ChangedRatio <= 0 {
		t.Fatalf("第二次收藏应带变化率: %+v", second)
	}

	hash8 := third.URLHash[:8]
	v1, v2, latest := versionRel(hash8, 1), versionRel(hash8, 2), third.RelativePath

	// 只靠 frontmatter 指针，从任意一份走到另外两份（§10 第 1 条）。
	if got := follow(t, dir, latest, "prevPath"); got != v2 {
		t.Errorf("最新版 prevPath → %q，期望 %q", got, v2)
	}
	if got := follow(t, dir, v2, "prevPath"); got != v1 {
		t.Errorf("v2 prevPath → %q，期望 %q", got, v1)
	}
	if got := follow(t, dir, v1, "nextPath"); got != v2 {
		t.Errorf("v1 nextPath → %q，期望 %q", got, v2)
	}
	if got := follow(t, dir, v2, "nextPath"); got != latest {
		t.Errorf("v2 nextPath → %q，期望 %q", got, latest)
	}
	if follow(t, dir, v1, "prevPath") != "" || follow(t, dir, latest, "nextPath") != "" {
		t.Error("链两端的指针应为 null")
	}
	if strings.Contains(strings.Join(readFront(t, dir, v2), "\n"), "tracking:") {
		t.Error("tracking 只该写在最新版上")
	}

	chain, ok := ReadChain(dir, third.URLHash)
	if !ok {
		t.Fatal("chain.json 不存在")
	}
	if chain.LatestVersion != 3 || chain.VersionCount != 3 || chain.ObservationCount != 3 {
		t.Errorf("chain 汇总不对: %+v", chain)
	}
	if chain.Versions[0].Version != 3 || chain.Versions[2].Version != 1 {
		t.Error("chain.versions 应按版本号降序")
	}
	if chain.Versions[0].Path != "../../"+latest || chain.Versions[1].Path != "v000002.md" {
		t.Errorf("chain path 不对: %q / %q", chain.Versions[0].Path, chain.Versions[1].Path)
	}
	if chain.FirstCapturedAt != "2026-08-01T09:00:00+08:00" || chain.LastChangedAt != "2026-08-03T09:00:00+08:00" {
		t.Errorf("chain 时间不对: first=%q lastChanged=%q", chain.FirstCapturedAt, chain.LastChangedAt)
	}

	entry := ReadIndex(dir)[0]
	if entry.CaptureCount != 3 || entry.VersionCount != 3 || entry.LatestVersion != 3 || entry.ChainPath != chainRel(hash8) {
		t.Errorf("索引版本字段不对: %+v", entry)
	}
	files, _ := os.ReadDir(filepath.Join(dir, filesDirName))
	if len(files) != 1 {
		t.Errorf("files/ 下应恰好一份正文，得到 %d", len(files))
	}
}

func TestVersionsUnchangedCaptureOnlyObserves(t *testing.T) {
	dir := t.TempDir()
	mustIngest(t, dir, capture("2026-08-01T09:00:00+08:00", "100"))
	mustIngest(t, dir, capture("2026-08-02T09:00:00+08:00", "120"))

	// R2：contentHash 相同。
	same := mustIngest(t, dir, capture("2026-08-03T09:00:00+08:00", "120"))
	if *same.VersionCreated || !*same.Unchanged || same.Version != 2 || same.CaptureCount != 3 {
		t.Fatalf("R2 回执不对: %+v", same)
	}
	// R3：contentHash 不同但只有排版差异。
	jitter := capture("2026-08-04T09:00:00+08:00", "120")
	jitter.ContentHash = "hash-jitter"
	jitter.Markdown = strings.ReplaceAll(jitter.Markdown, "\n\n", "\n\n\n") + "\n---\n"
	jittered := mustIngest(t, dir, jitter)
	if *jittered.VersionCreated || !*jittered.Unchanged {
		t.Fatalf("R3 应判为未变化: %+v", jittered)
	}

	front := readFront(t, dir, jittered.RelativePath)
	if frontInt(front, "observationCount") != 3 || frontString(front, "lastSeenAt") != "2026-08-04T09:00:00+08:00" {
		t.Errorf("未变化的收藏应只记观察:\n%s", strings.Join(front, "\n"))
	}
	if frontString(front, "capturedAt") != "2026-08-02T09:00:00+08:00" {
		t.Error("未变化的收藏不应改动正文的 capturedAt")
	}
	chain, _ := ReadChain(dir, jittered.URLHash)
	if chain.VersionCount != 2 || chain.ObservationCount != 4 || chain.LatestCapturedAt != "2026-08-04T09:00:00+08:00" {
		t.Errorf("chain 汇总不对: %+v", chain)
	}
}

func TestVersionsMergeWindowOverwritesLatest(t *testing.T) {
	dir := t.TempDir()
	mustIngest(t, dir, capture("2026-08-01T09:00:00+08:00", "100"))
	mustIngest(t, dir, capture("2026-08-02T09:00:00+08:00", "120"))

	// 5 分钟后，只在长正文末尾多一个字：变化率远低于 2%，就地补录。
	tweak := capture("2026-08-02T09:05:00+08:00", "120")
	tweak.ContentHash = "hash-tweak"
	var long strings.Builder
	for i := 0; i < 60; i++ {
		long.WriteString("固定行 " + string(rune('a'+i%26)) + string(rune('0'+i/26)) + "\n")
	}
	base := capture("2026-08-02T09:10:00+08:00", "120")
	base.ContentHash = "hash-long"
	base.Markdown += long.String()
	mustIngest(t, dir, base) // 越过阈值 → v3

	tweak.CapturedAt = "2026-08-02T09:15:00+08:00"
	tweak.Markdown = base.Markdown + "尾巴\n"
	merged := mustIngest(t, dir, tweak)
	if *merged.VersionCreated || *merged.Unchanged || merged.Version != 3 {
		t.Fatalf("R4 应就地补录 v3: %+v", merged)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, merged.RelativePath))
	if !strings.Contains(string(raw), "尾巴") {
		t.Error("补录后最新版正文应被替换")
	}

	// 越过合并窗口的同样微改动要切新版本。
	later := tweak
	later.CapturedAt = "2026-08-02T09:40:00+08:00"
	later.ContentHash = "hash-later"
	later.Markdown = tweak.Markdown + "又一个尾巴\n"
	if result := mustIngest(t, dir, later); !*result.VersionCreated || result.Version != 4 {
		t.Fatalf("合并窗口外应建新版本: %+v", result)
	}
}

func TestVersionsChainIsDerivedFromFrontmatter(t *testing.T) {
	dir := t.TempDir()
	mustIngest(t, dir, capture("2026-08-01T09:00:00+08:00", "100"))
	mustIngest(t, dir, capture("2026-08-02T09:00:00+08:00", "120"))
	last := mustIngest(t, dir, capture("2026-08-03T09:00:00+08:00", "150"))

	chainPath := filepath.Join(dir, filepath.FromSlash(chainRel(last.URLHash[:8])))
	before, err := os.ReadFile(chainPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(chainPath); err != nil {
		t.Fatal(err)
	}
	chain, err := BuildChain(dir, last.URLHash, last.RelativePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeChain(dir, chain); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(chainPath)
	if string(before) != string(after) {
		t.Errorf("从 frontmatter 重算的 chain.json 与原来不一致:\n%s\n---\n%s", before, after)
	}
}

func TestVersionsLateCaptureKeepsRealTime(t *testing.T) {
	dir := t.TempDir()
	mustIngest(t, dir, capture("2026-08-03T09:00:00+08:00", "100"))
	mustIngest(t, dir, capture("2026-08-04T09:00:00+08:00", "120"))
	// 离线队列里攒着的周一那次，周三才投到。
	late := mustIngest(t, dir, capture("2026-08-01T09:00:00+08:00", "090"))
	if late.Version != 3 {
		t.Fatalf("晚到的捕获照常追加: %+v", late)
	}
	front := readFront(t, dir, late.RelativePath)
	if raw, _ := frontRaw(front, "late"); raw != "true" {
		t.Error("晚到的版本应标 late: true")
	}
	if entry := ReadIndex(dir)[0]; entry.CapturedAt != "2026-08-04T09:00:00+08:00" {
		t.Errorf("索引 capturedAt 不应倒退: %q", entry.CapturedAt)
	}
}

func TestVersionsTitleChangeKeepsPointersValid(t *testing.T) {
	dir := t.TempDir()
	mustIngest(t, dir, capture("2026-08-01T09:00:00+08:00", "100"))
	mustIngest(t, dir, capture("2026-08-02T09:00:00+08:00", "120"))
	renamed := capture("2026-08-03T09:00:00+08:00", "150")
	renamed.Title = "销售看板 · 8 月"
	last := mustIngest(t, dir, renamed)

	hash8 := last.URLHash[:8]
	if got := follow(t, dir, versionRel(hash8, 2), "nextPath"); got != last.RelativePath {
		t.Errorf("换标题后 v2 nextPath → %q，期望 %q", got, last.RelativePath)
	}
	files, _ := os.ReadDir(filepath.Join(dir, filesDirName))
	if len(files) != 1 || files[0].Name() != filepath.Base(last.RelativePath) {
		t.Errorf("files/ 应只剩新文件名那一份")
	}
}

func TestVersionsBootstrapFromLegacyRecaptures(t *testing.T) {
	dir := t.TempDir()
	mustIngest(t, dir, capture("2026-08-01T09:00:00+08:00", "100"))
	// 模拟旧接收端留下的多次覆盖：captureCount=4，没有任何版本字段。
	entries := ReadIndex(dir)
	entries[0].CaptureCount = 4
	entries[0].CapturedAt = "2026-08-05T09:00:00+08:00"
	if err := writeIndex(dir, entries); err != nil {
		t.Fatal(err)
	}

	result := mustIngest(t, dir, capture("2026-08-06T09:00:00+08:00", "120"))
	v1 := readFront(t, dir, versionRel(result.URLHash[:8], 1))
	if frontString(v1, "note") != notePreVersioning || frontInt(v1, "observationCount") != 4 {
		t.Errorf("v1 应继承旧的观察次数并注明 pre-versioning:\n%s", strings.Join(v1, "\n"))
	}
	if frontString(v1, "firstSeenAt") != "2026-08-01T09:00:00+08:00" {
		t.Errorf("v1 firstSeenAt 应保留原始首次收藏时间")
	}
	if result.CaptureCount != 5 {
		t.Errorf("captureCount 仍是观察次数: %d", result.CaptureCount)
	}
}

func TestVersionsTrackingOff(t *testing.T) {
	dir := t.TempDir()
	first := mustIngest(t, dir, capture("2026-08-01T09:00:00+08:00", "100"))
	if _, found, err := SetTracking(dir, first.URLHash[:16], TrackingOff, ""); err != nil || !found {
		t.Fatalf("SetTracking: found=%v err=%v", found, err)
	}
	second := mustIngest(t, dir, capture("2026-08-02T09:00:00+08:00", "120"))
	if second.Version != 0 || second.Tracking != TrackingOff || second.CaptureCount != 2 {
		t.Fatalf("关闭追踪后应退回覆盖 + 计数: %+v", second)
	}
	if _, err := os.Stat(filepath.Join(dir, versionsDirName)); !os.IsNotExist(err) {
		t.Error("关闭追踪不应建版本目录")
	}

	// 已有链时关掉：历史保留，之后的收藏就地覆盖最新版。
	dir = t.TempDir()
	mustIngest(t, dir, capture("2026-08-01T09:00:00+08:00", "100"))
	tracked := mustIngest(t, dir, capture("2026-08-02T09:00:00+08:00", "120"))
	if _, _, err := SetTracking(dir, tracked.URLHash, TrackingOff, ""); err != nil {
		t.Fatal(err)
	}
	if chain, _ := ReadChain(dir, tracked.URLHash); chain.Tracking != TrackingOff {
		t.Errorf("chain.tracking 应同步为 off，得到 %q", chain.Tracking)
	}
	after := mustIngest(t, dir, capture("2026-08-03T09:00:00+08:00", "150"))
	if after.Version != 2 || *after.VersionCreated {
		t.Fatalf("关掉后应覆盖 v2 而不是建 v3: %+v", after)
	}
	if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(versionRel(tracked.URLHash[:8], 1)))); err != nil {
		t.Error("关掉追踪不应删除已有历史版本")
	}

	if _, _, err := SetTracking(dir, tracked.URLHash, "sometimes", ""); err == nil {
		t.Error("非法取值应报错")
	}
	if _, found, _ := SetTracking(dir, "ffffffffffff", TrackingOn, ""); found {
		t.Error("不存在的 hash 应返回 found=false")
	}
}

func TestBodyHashIgnoresLayoutOnly(t *testing.T) {
	a := "# 标题\n\n正文  一段。\n\n---\n\n| a | b |\n"
	b := "# 标题\r\n正文 一段。   \n\n\n***\n| a | b |\n\n"
	if BodyHash(a) != BodyHash(b) {
		t.Error("空白与装饰行差异不应改变 bodyHash")
	}
	if BodyHash(a) == BodyHash(strings.Replace(a, "一段", "两段", 1)) {
		t.Error("文字变化必须改变 bodyHash")
	}
	if len(BodyHash(a)) != 16 {
		t.Error("bodyHash 应为 16 位")
	}
}

func TestDiffCountsLines(t *testing.T) {
	change := Diff("a\nb\nc\n", "a\nc\nd\ne\n")
	if change.Added != 2 || change.Removed != 1 || change.Ratio != 0.4286 {
		t.Errorf("Diff = %+v", change)
	}
	if got := Diff("x\ny\n", "y\nx\n"); got.Ratio != 0 {
		t.Errorf("只换顺序不算变化: %+v", got)
	}
	if got := Diff("", ""); got.Ratio != 0 {
		t.Errorf("空对空: %+v", got)
	}
}

func TestVersionsTrackingTravelsWithTheCapture(t *testing.T) {
	// 第一次收藏就显式打开：这一份直接是链上的 v1。
	dir := t.TempDir()
	on := capture("2026-08-01T09:00:00+08:00", "100")
	on.Tracking = TrackingOn
	first := mustIngest(t, dir, on)
	if first.Version != 1 || first.Tracking != TrackingOn {
		t.Fatalf("显式开启的首次收藏回执不对: %+v", first)
	}
	chain, ok := ReadChain(dir, first.URLHash)
	if !ok || chain.VersionCount != 1 || chain.Tracking != TrackingOn {
		t.Fatalf("显式开启应立即建链: ok=%v %+v", ok, chain)
	}
	if second := mustIngest(t, dir, capture("2026-08-02T09:00:00+08:00", "120")); second.Version != 2 {
		t.Fatalf("不带开关的后续收藏沿用 on: %+v", second)
	}

	// 第二次收藏带着 off：不建链，退回覆盖 + 计数。
	dir = t.TempDir()
	mustIngest(t, dir, capture("2026-08-01T09:00:00+08:00", "100"))
	off := capture("2026-08-02T09:00:00+08:00", "120")
	off.Tracking = TrackingOff
	if result := mustIngest(t, dir, off); result.Version != 0 || result.Tracking != TrackingOff {
		t.Fatalf("带 off 的收藏应退回旧行为: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(dir, versionsDirName)); !os.IsNotExist(err) {
		t.Error("带 off 的收藏不应建版本目录")
	}
	if entry := ReadIndex(dir)[0]; entry.Tracking != TrackingOff {
		t.Errorf("开关应记进索引: %q", entry.Tracking)
	}

	// 再带着 on 收一次：重新开始记版本，磁盘上那份成为 v1。
	back := capture("2026-08-03T09:00:00+08:00", "150")
	back.Tracking = TrackingOn
	if result := mustIngest(t, dir, back); result.Version != 2 || !*result.VersionCreated {
		t.Fatalf("重新打开后应切出 v2: %+v", result)
	}

	// 非法取值当作没带。
	junk := capture("2026-08-04T09:00:00+08:00", "180")
	junk.Tracking = "sometimes"
	if result := mustIngest(t, dir, junk); result.Tracking != TrackingOn || result.Version != 3 {
		t.Fatalf("非法开关值应被忽略: %+v", result)
	}
}
