package webpage

import (
	"fmt"
	"strings"
	"testing"
)

func render(ops []LineOp) string {
	var b strings.Builder
	for _, op := range ops {
		if op.Kind == 0 {
			b.WriteString("@@\n")
			continue
		}
		b.WriteByte(op.Kind)
		b.WriteString(op.Text + "\n")
	}
	return b.String()
}

func TestDiffBodiesShowsChangesWithContext(t *testing.T) {
	var before, after strings.Builder
	for i := 0; i < 20; i++ {
		line := fmt.Sprintf("行 %02d", i)
		before.WriteString(line + "\n\n")
		switch i {
		case 5:
			after.WriteString("行 05 已修改\n\n")
		case 15:
			after.WriteString(line + "\n\n新增的一行\n\n")
		default:
			after.WriteString(line + "\n\n")
		}
	}
	diff := DiffBodies(before.String(), after.String(), 1)
	if diff.Added != 2 || diff.Removed != 1 || diff.Approximate {
		t.Fatalf("计数不对: %+v", diff)
	}
	want := "" +
		" 行 04\n-行 05\n+行 05 已修改\n 行 06\n" +
		"@@\n" +
		" 行 15\n+新增的一行\n 行 16\n"
	if got := render(diff.Ops); got != want {
		t.Errorf("patch 不对:\n%s\n期望:\n%s", got, want)
	}
}

func TestDiffBodiesIdenticalHasNoOps(t *testing.T) {
	if diff := DiffBodies("a\nb\n", "a\nb\n", 3); len(diff.Ops) != 0 || diff.Added+diff.Removed != 0 {
		t.Errorf("相同正文不应有输出: %+v", diff)
	}
}

func TestDiffBodiesIgnoresLayoutOnly(t *testing.T) {
	diff := DiffBodies("a  b\n\nc\n", "a b\nc\n\n\n", 2)
	if diff.Added != 0 || diff.Removed != 0 || len(diff.Ops) != 0 {
		t.Errorf("只改排版不应有差异: %+v", diff)
	}
}

func TestDiffBodiesFallsBackWhenTooLarge(t *testing.T) {
	var before, after strings.Builder
	for i := 0; i < maxDiffEdits+10; i++ {
		fmt.Fprintf(&before, "旧 %d\n", i)
		fmt.Fprintf(&after, "新 %d\n", i)
	}
	diff := DiffBodies(before.String(), after.String(), 2)
	if !diff.Approximate || diff.Added != maxDiffEdits+10 || diff.Removed != maxDiffEdits+10 {
		t.Errorf("整页替换应退回近似清单: approximate=%v +%d -%d", diff.Approximate, diff.Added, diff.Removed)
	}
}

func TestDiffBodiesAgreesWithMyersOnEdgeCases(t *testing.T) {
	cases := []struct{ a, b string }{
		{"", "x\n"},
		{"x\n", ""},
		{"a\nb\nc\n", "c\nb\na\n"},
	}
	for _, c := range cases {
		diff := DiffBodies(c.a, c.b, 3)
		aKeys, _ := diffLines(c.a)
		bKeys, _ := diffLines(c.b)
		// 应用编辑后能从 a 得到 b：保留行 + 新增行 = b 的行数。
		kept := 0
		for _, op := range diff.Ops {
			if op.Kind == ' ' {
				kept++
			}
		}
		if kept+diff.Added != len(bKeys) || kept+diff.Removed != len(aKeys) {
			t.Errorf("%q → %q: kept=%d +%d -%d", c.a, c.b, kept, diff.Added, diff.Removed)
		}
	}
}
