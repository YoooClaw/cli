package webpage

import (
	"fmt"
	"strings"
)

// versionedIngest 处理「同一 URL 已经收过」的那次收藏：按 R1–R5 决定是只记观察、
// 就地补录，还是切出新版本（web-page-versions-prd §3.1 / §5.1）。
type versionedIngest struct {
	dir          string
	payload      Payload
	canonical    string
	capturedAt   string
	clientLabel  string
	urlHash      string
	relativePath string // 本次标题算出的最新版文件名
	previous     *Entry
	logger       Logger
}

type ingestMode int

const (
	modeUnchanged ingestMode = iota // R2 / R3：只记一次观察
	modeOverwrite                   // R1 / R4：就地替换最新版，不增版本号
	modeCreate                      // R5：切出新版本
)

// run 返回 handled=false 表示最新版文件读不到或不认识，调用方退回单版本逻辑——
// 索引可能被手工改过，宁可按旧行为覆盖，也不在一份残缺的链上继续长。
func (v versionedIngest) run() (IngestResult, Entry, bool, error) {
	hash8 := v.urlHash[:8]
	latestRel := v.previous.RelativePath
	latestDoc, err := readRelative(v.dir, latestRel)
	if err != nil {
		return IngestResult{}, Entry{}, false, nil
	}
	front, body, ok := splitDocument(latestDoc)
	if !ok {
		return IngestResult{}, Entry{}, false, nil
	}
	tracking := trackingOf(v.previous)
	latest, hasMeta := parseMeta(front)
	if !hasMeta {
		if tracking == TrackingOff {
			return IngestResult{}, Entry{}, false, nil
		}
		// 自动开启：磁盘上已有的那份就是 v1，链天生完整（§3.2）。旧的重复收藏
		// 没留正文，只能把次数记进 v1，并注明之前的观察没有正文可回溯（§9）。
		latest = versionMeta{
			Version:          1,
			BodyHash:         BodyHash(body),
			ObservationCount: max(v.previous.CaptureCount, 1),
			FirstSeenAt:      firstNonEmpty(v.previous.FirstCapturedAt, frontString(front, "capturedAt")),
			LastSeenAt:       firstNonEmpty(v.previous.CapturedAt, frontString(front, "capturedAt")),
			Source:           sourceManual,
		}
		if v.previous.CaptureCount > 1 {
			latest.Note = notePreVersioning
		}
	}
	latest.Tracking = tracking
	latestCapturedAt := frontString(front, "capturedAt")

	incomingDoc := BuildDocument(v.payload, v.canonical, v.capturedAt, v.clientLabel, "")
	_, incomingBody, _ := splitDocument(incomingDoc)
	incomingHash := strings.TrimSpace(v.payload.ContentHash)

	mode, change := modeCreate, Change{}
	switch {
	case tracking == TrackingOff:
		mode = modeOverwrite // R1
	case incomingHash != "" && incomingHash == frontString(front, "contentHash"):
		mode = modeUnchanged // R2
	case BodyHash(incomingBody) == latest.BodyHash:
		mode = modeUnchanged // R3
	default:
		change = Diff(body, incomingBody)
		if withinMergeWindow(latestCapturedAt, v.capturedAt) && change.Ratio < MergeRatio {
			mode = modeOverwrite // R4
		}
	}

	var out outcome
	switch mode {
	case modeUnchanged:
		out, err = v.observe(front, body, latest, latestRel)
	case modeOverwrite:
		out, err = v.overwrite(latest, latestRel, incomingBody)
	default:
		out, err = v.create(front, body, latest, latestRel, latestCapturedAt, incomingBody, change)
	}
	if err != nil {
		return IngestResult{}, Entry{}, true, err
	}

	chain, err := BuildChain(v.dir, v.urlHash, out.relativePath)
	if err != nil {
		return IngestResult{}, Entry{}, true, fmt.Errorf("重算版本链失败：%w", err)
	}
	if err := writeChain(v.dir, chain); err != nil {
		return IngestResult{}, Entry{}, true, err
	}

	entry := *v.previous
	if mode != modeUnchanged {
		entry.URL = strings.TrimSpace(v.payload.URL)
		entry.Title = v.payload.Title
		entry.SiteName = strings.TrimSpace(v.payload.SiteName)
		entry.ClientLabel = v.clientLabel
		entry.ContentHash = incomingHash
	}
	entry.RelativePath = out.relativePath
	entry.CapturedAt = laterOf(v.previous.CapturedAt, v.capturedAt)
	if entry.FirstCapturedAt == "" {
		entry.FirstCapturedAt = chain.FirstCapturedAt
	}
	entry.CaptureCount = v.previous.CaptureCount + 1
	entry.Bytes = out.bytes
	entry.ArchivePath, entry.ArchiveBytes = out.archivePath, out.archiveBytes
	entry.VersionCount = chain.VersionCount
	entry.LatestVersion = chain.LatestVersion
	entry.ChainPath = chainRel(hash8)
	entry.LastChangedAt = chain.LastChangedAt

	created, unchanged := mode == modeCreate, mode == modeUnchanged
	result := IngestResult{
		OK:             true,
		URLHash:        v.urlHash,
		RelativePath:   out.relativePath,
		ArchivePath:    out.archivePath,
		Replaced:       true,
		CaptureCount:   entry.CaptureCount,
		Version:        chain.LatestVersion,
		VersionCreated: &created,
		Unchanged:      &unchanged,
		VersionCount:   chain.VersionCount,
		Tracking:       tracking,
	}
	if out.changed != nil {
		ratio := out.changed.Ratio
		result.ChangedRatio = &ratio
	}

	verb := map[ingestMode]string{modeUnchanged: "内容未变化", modeOverwrite: "补录", modeCreate: "新版本"}[mode]
	v.logger.Info(fmt.Sprintf("web-page[%s] %s：v%d（第 %d 次收藏）", hash8, verb, chain.LatestVersion, entry.CaptureCount))
	return result, entry, true, nil
}

type outcome struct {
	relativePath string
	bytes        int
	archivePath  string
	archiveBytes int
	changed      *Change
}

// observe（R2/R3）只把这次收藏记成一次观察：正文与文件名都不动。
func (v versionedIngest) observe(front []string, body string, latest versionMeta, latestRel string) (outcome, error) {
	latest.ObservationCount++
	latest.LastSeenAt = laterOf(latest.LastSeenAt, v.capturedAt)

	archivePath, archiveBytes := v.previous.ArchivePath, v.previous.ArchiveBytes
	if v.payload.Archive != nil {
		// 内容没变但带了新存档：存档照收（它是重处理原料），坏存档则保留旧的。
		if placed, size := placeArchive(v.dir, v.payload.Archive, latestRel, nil, v.urlHash[:8], v.logger); placed != "" {
			archivePath, archiveBytes = placed, size
			front = withFrontString(front, "archive", placed)
		}
	}

	links := linkTarget{hash8: v.urlHash[:8], latestVersion: latest.Version, latestRel: latestRel}
	doc := joinDocument(renderMeta(front, latest, latestRel, links), body)
	if err := writeRelative(v.dir, latestRel, doc); err != nil {
		return outcome{}, err
	}
	return outcome{relativePath: latestRel, bytes: len(doc), archivePath: archivePath, archiveBytes: archiveBytes}, nil
}

// overwrite（R1/R4）替换最新版正文，版本号不变；变化量重新对前一版算。
func (v versionedIngest) overwrite(latest versionMeta, latestRel, incomingBody string) (outcome, error) {
	hash8 := v.urlHash[:8]
	meta := latest
	meta.BodyHash = BodyHash(incomingBody)
	meta.ObservationCount++
	meta.LastSeenAt = laterOf(latest.LastSeenAt, v.capturedAt)
	meta.Changed = nil
	if latest.Prev > 0 {
		if prevDoc, err := readRelative(v.dir, versionRel(hash8, latest.Prev)); err == nil {
			if _, prevBody, ok := splitDocument(prevDoc); ok {
				change := Diff(prevBody, incomingBody)
				meta.Changed = &change
			}
		}
	}

	newRel := v.relativePath
	links := linkTarget{hash8: hash8, latestVersion: meta.Version, latestRel: newRel}
	out, err := v.writeLatest(meta, newRel, links)
	if err != nil {
		return outcome{}, err
	}
	if newRel != latestRel {
		removeFile(v.dir, latestRel)
		if latest.Prev > 0 {
			v.relink(latest.Prev, links)
		}
	}
	out.changed = meta.Changed
	return out, nil
}

// create（R5）切出新版本。写入顺序保证中断后磁盘上始终有一份最新正文：
// ① 先把当前最新版复制成 versions/v<n>.md；② 原子替换 files/ 下的最新版；
// ③ 标题变了才删旧文件名；④ 回写前一版的 next 指针。chain.json 与索引由调用方最后写。
func (v versionedIngest) create(front []string, body string, latest versionMeta, latestRel, latestCapturedAt, incomingBody string, change Change) (outcome, error) {
	hash8 := v.urlHash[:8]
	next := latest.Version + 1
	newRel := v.relativePath
	links := linkTarget{hash8: hash8, latestVersion: next, latestRel: newRel}

	history := latest
	history.Tracking = ""
	history.Next = next
	// 存档只跟最新版（§7），历史版本不再指向它。
	historyFront := withoutKeys(front, "archive")
	historyRel := versionRel(hash8, latest.Version)
	historyDoc := joinDocument(renderMeta(historyFront, history, historyRel, links), body)
	if err := writeRelative(v.dir, historyRel, historyDoc); err != nil {
		return outcome{}, err
	}

	meta := versionMeta{
		Tracking:         latest.Tracking,
		Version:          next,
		Prev:             latest.Version,
		BodyHash:         BodyHash(incomingBody),
		ObservationCount: 1,
		FirstSeenAt:      v.capturedAt,
		LastSeenAt:       v.capturedAt,
		Changed:          &change,
		Source:           sourceManual,
		// 离线补投：版本号按落盘顺序发，capturedAt 却是真实的旧时间（§4.4）。
		Late: before(v.capturedAt, latestCapturedAt),
	}
	out, err := v.writeLatest(meta, newRel, links)
	if err != nil {
		return outcome{}, err
	}
	if newRel != latestRel {
		removeFile(v.dir, latestRel)
	}
	if latest.Prev > 0 {
		v.relink(latest.Prev, links)
	}
	out.changed = meta.Changed
	return out, nil
}

// writeLatest 写 files/ 下的最新版，并让存档跟上新文件名。
func (v versionedIngest) writeLatest(meta versionMeta, newRel string, links linkTarget) (outcome, error) {
	hash8 := v.urlHash[:8]
	archivePath, archiveBytes := placeArchive(v.dir, v.payload.Archive, newRel, v.previous, hash8, v.logger)
	built := BuildDocument(v.payload, v.canonical, v.capturedAt, v.clientLabel, archivePath)
	front, body, _ := splitDocument(built)
	doc := joinDocument(renderMeta(front, meta, newRel, links), body)
	if err := writeRelative(v.dir, newRel, doc); err != nil {
		return outcome{}, err
	}
	if v.previous.ArchivePath != "" && v.previous.ArchivePath != archivePath {
		removeFile(v.dir, v.previous.ArchivePath)
	}
	return outcome{relativePath: newRel, bytes: len(doc), archivePath: archivePath, archiveBytes: archiveBytes}, nil
}

// relink 重写一份历史版本的指针路径（最新版换了文件名、或它的 next 从 files/ 挪进了 versions/）。
// 失败只记日志：指针路径可以从版本号重算，不值得让一次收藏失败。
func (v versionedIngest) relink(version int, links linkTarget) {
	rel := versionRel(links.hash8, version)
	doc, err := readRelative(v.dir, rel)
	if err == nil {
		if front, body, ok := splitDocument(doc); ok {
			if meta, ok := parseMeta(front); ok {
				err = writeRelative(v.dir, rel, joinDocument(renderMeta(front, meta, rel, links), body))
			}
		}
	}
	if err != nil {
		v.logger.Warn(fmt.Sprintf("web-page[%s] 回写 v%d 指针失败：%v", links.hash8, version, err))
	}
}

// startChain 把刚落盘的首份正文标成 v1 并建链，用于第一次收藏时就显式打开追踪。
func startChain(dir string, entry *Entry) error {
	doc, err := readRelative(dir, entry.RelativePath)
	if err != nil {
		return err
	}
	front, body, ok := splitDocument(doc)
	if !ok {
		return fmt.Errorf("首份正文缺少 frontmatter")
	}
	capturedAt := frontString(front, "capturedAt")
	meta := versionMeta{
		Tracking:         entry.Tracking,
		Version:          1,
		BodyHash:         BodyHash(body),
		ObservationCount: 1,
		FirstSeenAt:      capturedAt,
		LastSeenAt:       capturedAt,
		Source:           sourceManual,
	}
	links := linkTarget{hash8: entry.URLHash[:8], latestVersion: 1, latestRel: entry.RelativePath}
	doc = joinDocument(renderMeta(front, meta, entry.RelativePath, links), body)
	if err := writeRelative(dir, entry.RelativePath, doc); err != nil {
		return err
	}
	chain, err := BuildChain(dir, entry.URLHash, entry.RelativePath)
	if err != nil {
		return err
	}
	if err := writeChain(dir, chain); err != nil {
		return err
	}
	entry.Bytes = len(doc)
	entry.VersionCount, entry.LatestVersion = chain.VersionCount, chain.LatestVersion
	entry.ChainPath = chainRel(entry.URLHash[:8])
	entry.LastChangedAt = chain.LastChangedAt
	return nil
}

// SetTracking 设置某个已收藏网页的追踪开关（POST /web-pages/tracking）。hash 可以是
// 完整 urlHash 或扩展本地索引那种 16 位前缀。已有链时同步改最新版 frontmatter 与 chain.json；
// 关掉追踪不删历史，已有版本留着，之后的收藏按 R1 覆盖最新版（§3.2）。
func SetTracking(dir, hash, tracking, scope string) (Entry, bool, error) {
	if !ValidTracking(tracking) {
		return Entry{}, false, invalid("tracking 只接受 auto / on / off")
	}
	query := strings.ToLower(strings.TrimSpace(hash))
	if len(query) < 8 {
		return Entry{}, false, invalid("hash 至少 8 位")
	}

	writeMu.Lock()
	defer writeMu.Unlock()

	entries := ReadIndex(dir)
	idx := -1
	for i, entry := range entries {
		if strings.HasPrefix(strings.ToLower(entry.URLHash), query) && len(FilterByScope([]Entry{entry}, scope)) == 1 {
			idx = i
			break
		}
	}
	if idx < 0 {
		return Entry{}, false, nil
	}
	entry := &entries[idx]
	entry.Tracking = tracking

	if entry.ChainPath != "" && len(entry.URLHash) >= 8 {
		if doc, err := readRelative(dir, entry.RelativePath); err == nil {
			if front, body, ok := splitDocument(doc); ok {
				if meta, ok := parseMeta(front); ok {
					meta.Tracking = tracking
					links := linkTarget{hash8: entry.URLHash[:8], latestVersion: meta.Version, latestRel: entry.RelativePath}
					if err := writeRelative(dir, entry.RelativePath, joinDocument(renderMeta(front, meta, entry.RelativePath, links), body)); err != nil {
						return Entry{}, true, err
					}
					if chain, err := BuildChain(dir, entry.URLHash, entry.RelativePath); err == nil {
						if err := writeChain(dir, chain); err != nil {
							return Entry{}, true, err
						}
					}
				}
			}
		}
	}
	if err := writeIndex(dir, entries); err != nil {
		return Entry{}, true, err
	}
	return *entry, true, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
