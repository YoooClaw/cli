package webpage

import (
	"strings"
)

// 行级 diff，供 `yoooclaw synced-web-page diff` 把两版正文的差异直接交给 agent，
// 省得它为了回答「变了什么」把两份全文都读进上下文。
//
// 比较前对每行做与 bodyHash 相同的空白折叠并丢掉空行：只动排版的行不算变化，
// 这样 diff 与 changedFromPrev、bodyHash 的口径一致。输出保留原文。

const (
	// maxDiffLines 与 maxDiffEdits 限住 Myers 的时间与回溯内存（约 maxDiffEdits² 个 int）。
	// 超限时退回不带上下文的「删了哪些、加了哪些」，并标 approximate。
	maxDiffLines = 20000
	maxDiffEdits = 2000
)

// LineOp 是 diff 的一行：Kind 为 ' '（上下文）、'-'（只在旧版）、'+'（只在新版），
// Kind 为 0 表示两个 hunk 之间省略的上下文。
type LineOp struct {
	Kind byte
	Text string
}

// LineDiff 是两段正文的行级差异。
type LineDiff struct {
	Ops         []LineOp
	Added       int
	Removed     int
	Approximate bool // 超出 Myers 上限，退回了无序的增删清单
}

func diffLines(body string) (keys, texts []string) {
	for _, line := range strings.Split(body, "\n") {
		key := strings.Join(strings.Fields(line), " ")
		if key == "" {
			continue
		}
		keys = append(keys, key)
		texts = append(texts, strings.TrimRight(line, " \t\r"))
	}
	return keys, texts
}

// DiffBodies 算 before → after 的行级差异，每个 hunk 前后保留 context 行上下文。
func DiffBodies(before, after string, context int) LineDiff {
	aKeys, aTexts := diffLines(before)
	bKeys, bTexts := diffLines(after)

	// 先剥掉公共首尾：数据页通常只改中间几行，这一步让 Myers 只跑在很小的区间上。
	prefix := 0
	for prefix < len(aKeys) && prefix < len(bKeys) && aKeys[prefix] == bKeys[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(aKeys)-prefix && suffix < len(bKeys)-prefix &&
		aKeys[len(aKeys)-1-suffix] == bKeys[len(bKeys)-1-suffix] {
		suffix++
	}
	midA, midB := aKeys[prefix:len(aKeys)-suffix], bKeys[prefix:len(bKeys)-suffix]

	var full []LineOp
	for i := 0; i < prefix; i++ {
		full = append(full, LineOp{Kind: ' ', Text: aTexts[i]})
	}
	middle, ok := myers(midA, midB)
	if !ok {
		return approximateDiff(aKeys, aTexts, bKeys, bTexts)
	}
	for _, op := range middle {
		switch op.kind {
		case ' ':
			full = append(full, LineOp{Kind: ' ', Text: aTexts[prefix+op.a]})
		case '-':
			full = append(full, LineOp{Kind: '-', Text: aTexts[prefix+op.a]})
		case '+':
			full = append(full, LineOp{Kind: '+', Text: bTexts[prefix+op.b]})
		}
	}
	for i := len(aKeys) - suffix; i < len(aKeys); i++ {
		full = append(full, LineOp{Kind: ' ', Text: aTexts[i]})
	}

	result := LineDiff{}
	for _, op := range full {
		switch op.Kind {
		case '+':
			result.Added++
		case '-':
			result.Removed++
		}
	}
	result.Ops = withContext(full, context)
	return result
}

// withContext 只留改动行及其前后 context 行，被省掉的连续上下文折成一个 Kind=0 的分隔。
func withContext(full []LineOp, context int) []LineOp {
	keep := make([]bool, len(full))
	for i, op := range full {
		if op.Kind == ' ' {
			continue
		}
		for j := max(0, i-context); j <= min(len(full)-1, i+context); j++ {
			keep[j] = true
		}
	}
	var out []LineOp
	gap := false
	for i, op := range full {
		if !keep[i] {
			gap = true
			continue
		}
		if gap && len(out) > 0 {
			out = append(out, LineOp{})
		}
		gap = false
		out = append(out, op)
	}
	return out
}

type editOp struct {
	kind byte
	a, b int
}

// myers 是 Myers O((N+M)D) 最短编辑脚本。编辑数超过 maxDiffEdits 时放弃（ok=false）。
func myers(a, b []string) ([]editOp, bool) {
	n, m := len(a), len(b)
	if n+m > maxDiffLines {
		return nil, false
	}
	offset := n + m + 1
	v := make([]int, 2*offset+1)
	var snaps [][]int // snaps[d][k+d] = 第 d 轮结束时对角线 k 上最远的 x

	final := -1
	for d := 0; d <= n+m && d <= maxDiffEdits; d++ {
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[offset+k-1] < v[offset+k+1]) {
				x = v[offset+k+1]
			} else {
				x = v[offset+k-1] + 1
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[offset+k] = x
		}
		snaps = append(snaps, append([]int(nil), v[offset-d:offset+d+1]...))
		if v[offset+n-m] >= n && n-m >= -d && n-m <= d {
			final = d
			break
		}
	}
	if final < 0 {
		return nil, false
	}

	var ops []editOp
	x, y := n, m
	for d := final; d > 0; d-- {
		prev := snaps[d-1]
		at := func(k int) int { return prev[k+d-1] }
		k := x - y
		var prevK int
		if k == -d || (k != d && at(k-1) < at(k+1)) {
			prevK = k + 1
		} else {
			prevK = k - 1
		}
		prevX := at(prevK)
		prevY := prevX - prevK
		for x > prevX && y > prevY {
			x--
			y--
			ops = append(ops, editOp{kind: ' ', a: x, b: y})
		}
		if x == prevX {
			y--
			ops = append(ops, editOp{kind: '+', b: y})
		} else {
			x--
			ops = append(ops, editOp{kind: '-', a: x})
		}
	}
	for x > 0 && y > 0 {
		x--
		y--
		ops = append(ops, editOp{kind: ' ', a: x, b: y})
	}
	for i, j := 0, len(ops)-1; i < j; i, j = i+1, j-1 {
		ops[i], ops[j] = ops[j], ops[i]
	}
	return ops, true
}

// approximateDiff 在变化太大、Myers 放弃时给出多重集合口径的增删清单（与 Diff 的计数一致），
// 不带上下文也不保证顺序——这种规模的变化，本来也更接近「整页换了」。
func approximateDiff(aKeys, aTexts, bKeys, bTexts []string) LineDiff {
	pool := map[string]int{}
	for _, key := range bKeys {
		pool[key]++
	}
	result := LineDiff{Approximate: true}
	for i, key := range aKeys {
		if pool[key] > 0 {
			pool[key]--
			continue
		}
		result.Ops = append(result.Ops, LineOp{Kind: '-', Text: aTexts[i]})
		result.Removed++
	}
	pool = map[string]int{}
	for _, key := range aKeys {
		pool[key]++
	}
	for i, key := range bKeys {
		if pool[key] > 0 {
			pool[key]--
			continue
		}
		result.Ops = append(result.Ops, LineOp{Kind: '+', Text: bTexts[i]})
		result.Added++
	}
	return result
}
