package yclib

import (
	"time"

	"github.com/YoooClaw/cli/internal/errs"
	"github.com/YoooClaw/cli/internal/notif"
)

// newInvalidArg 构造一个 CodeInvalidArgument 的结构化错误。
func newInvalidArg(msg string) error { return errs.New(errs.CodeInvalidArgument, msg) }

// newNotFound 构造一个 CodeNotFound 的结构化错误。
func newNotFound(msg string) error { return errs.New(errs.CodeNotFound, msg) }

// labelOrLegacy 把空 clientLabel 归一为 "legacy"（对齐 CLI 的过滤语义）。
func labelOrLegacy(label string) string {
	if label == "" {
		return "legacy"
	}
	return label
}

// sliceN 取前 n 条（n 越界则原样返回）。
func sliceN(items []notif.StoredNotification, n int) []notif.StoredNotification {
	if n < len(items) {
		return items[:n]
	}
	return items
}

func localToday() string        { return time.Now().Format("2006-01-02") }
func localDaysAgo(n int) string { return time.Now().AddDate(0, 0, -n).Format("2006-01-02") }
