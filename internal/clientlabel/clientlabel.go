// Package clientlabel 统一「一条数据属于哪个客户端」的判定。
//
// 各 store 用 clientLabel 记录数据来源（api-key 的 label，也就是一条 Relay
// 隧道）。读取侧要按来源隔离时，判定规则必须三处一致——录音、网页、图片各写
// 一遍迟早会走偏，所以集中在这里。
package clientlabel

import "strings"

// Shared 判断 label 是否属于「没有明确来源」的数据：
//   - ""        —— 0.9.0 之前入库的老条目，那时写入路径还没带上 label；
//   - "default" —— 各 store 对空 label 的归一值；
//   - "legacy"  —— 查询侧对空 label 的显示名。
//
// 这三类一律对所有客户端可见：升级前的存量数据几乎全是 default，按来源严格
// 隔离会让它们从手机端整片消失。
func Shared(label string) bool {
	switch strings.ToLower(strings.TrimSpace(label)) {
	case "", "default", "legacy":
		return true
	}
	return false
}

// Visible 判断 entryLabel 的条目在 scope 视角下是否可见。
// scope 为空表示本机/宿主视角（CLI、loopback、gateway token），不做隔离。
func Visible(entryLabel, scope string) bool {
	scope = strings.TrimSpace(scope)
	if scope == "" || Shared(scope) {
		return true
	}
	if Shared(entryLabel) {
		return true
	}
	return strings.TrimSpace(entryLabel) == scope
}
