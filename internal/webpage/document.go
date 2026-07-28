package webpage

import (
	"fmt"
	"strings"
)

// BuildDocument 生成落盘的 .md：YAML frontmatter + 正文。
//
// 选 frontmatter 是为了文件自描述——Agent grep/cat 到任意一个文件都能立刻
// 知道出处，不必回查 index.json（PRD 3.5.4 硬要求「必须带出处」）。
func BuildDocument(payload Payload, canonicalURL, capturedAt, clientLabel, archivePath string) string {
	var b strings.Builder
	b.WriteString("---\n")
	writeYAMLString(&b, "url", strings.TrimSpace(payload.URL))
	writeYAMLString(&b, "canonicalUrl", canonicalURL)
	writeYAMLString(&b, "title", strings.TrimSpace(payload.Title))
	writeYAMLString(&b, "siteName", strings.TrimSpace(payload.SiteName))
	writeYAMLString(&b, "byline", strings.TrimSpace(payload.Byline))
	writeYAMLString(&b, "language", strings.TrimSpace(payload.Language))
	writeYAMLString(&b, "publishedTime", strings.TrimSpace(payload.PublishedTime))
	writeYAMLString(&b, "capturedAt", capturedAt)
	writeYAMLString(&b, "contentHash", strings.TrimSpace(payload.ContentHash))
	writeYAMLString(&b, "clientLabel", clientLabel)
	fmt.Fprintf(&b, "truncated: %t\n", payload.Truncated)
	fmt.Fprintf(&b, "lowConfidence: %t\n", payload.LowConfidence)
	writeYAMLString(&b, "archive", archivePath)
	b.WriteString("---\n\n")

	body := strings.TrimRight(payload.Markdown, " \t\r\n")
	if title := strings.TrimSpace(payload.Title); title != "" && !startsWithH1(body) {
		// Readability 把标题摘进 article.title，正文里通常没有 H1。补一个，
		// 让 Agent 直接 cat 文件时第一眼就看到这是哪篇。
		b.WriteString("# ")
		b.WriteString(collapseWhitespace(title))
		b.WriteString("\n\n")
	}
	b.WriteString(body)
	b.WriteString("\n")
	return b.String()
}

func startsWithH1(markdown string) bool {
	for _, line := range strings.Split(markdown, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		return strings.HasPrefix(trimmed, "# ")
	}
	return false
}

// writeYAMLString 空值整行省略，非空值一律双引号。
//
// 一律加引号是刻意的：标题里带 `: ` 或以 `- ` 开头都会让不加引号的 YAML
// 解析成别的东西，而「什么时候需要加引号」的判定规则要在 Go / TS / Python
// 三端写得完全一致，成本远高于统一加引号带来的那点可读性损失。
func writeYAMLString(b *strings.Builder, key, value string) {
	if value == "" {
		return
	}
	b.WriteString(key)
	b.WriteString(": ")
	b.WriteString(quoteYAML(value))
	b.WriteString("\n")
}

func quoteYAML(value string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range value {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04X`, r)
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func collapseWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
