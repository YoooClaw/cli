package webpage

import (
	"path"
	"strings"
)

// 读侧：把一条索引记录的版本链解析成可读的文件，供 CLI 与 agent 查询历史。
// 只读，从不写 chain.json——修链是写侧的事。

// VersionFile 是链上一个版本，Path 为 web-pages 根下的相对路径。
type VersionFile struct {
	ChainVersion
	RelativePath string
	Current      bool
}

// LoadChain 读一条网页的版本链。chain.json 缺失或损坏时从 frontmatter 现算（不落盘）；
// 从没建过链的网页返回 false——它只有一份正文，没有历史。
func LoadChain(dir string, entry Entry) (Chain, bool) {
	if entry.ChainPath == "" || len(entry.URLHash) < 8 {
		return Chain{}, false
	}
	if chain, ok := ReadChain(dir, entry.URLHash); ok && len(chain.Versions) > 0 {
		return chain, true
	}
	chain, err := BuildChain(dir, entry.URLHash, entry.RelativePath)
	return chain, err == nil && len(chain.Versions) > 0
}

// VersionFiles 按收藏时间升序列出链上每个版本——趋势要按 capturedAt 排，不能按版本号，
// 离线晚到的版本号大、时间早（§4.4）。路径越出 web-pages 根的记录直接丢弃。
func VersionFiles(chain Chain) []VersionFile {
	base := versionsDirName + "/" + chain.URLHash[:8]
	files := make([]VersionFile, 0, len(chain.Versions))
	for _, v := range chain.Versions {
		rel := path.Clean(path.Join(base, v.Path))
		if rel == "." || strings.HasPrefix(rel, "../") || strings.HasPrefix(rel, "/") {
			continue
		}
		files = append(files, VersionFile{ChainVersion: v, RelativePath: rel, Current: v.Version == chain.LatestVersion})
	}
	sortVersionFiles(files)
	return files
}

func sortVersionFiles(files []VersionFile) {
	for i := 1; i < len(files); i++ {
		for j := i; j > 0 && versionAfter(files[j-1], files[j]); j-- {
			files[j-1], files[j] = files[j], files[j-1]
		}
	}
}

// versionAfter 判断 a 是否应排在 b 之后：先比 capturedAt，比不出来（缺失/相等）再比版本号。
func versionAfter(a, b VersionFile) bool {
	if before(b.CapturedAt, a.CapturedAt) {
		return true
	}
	if before(a.CapturedAt, b.CapturedAt) {
		return false
	}
	return a.Version > b.Version
}

// BodyOf 取一份版本文件 frontmatter 之后的正文。
func BodyOf(document string) string {
	if _, body, ok := splitDocument(document); ok {
		return body
	}
	return document
}
