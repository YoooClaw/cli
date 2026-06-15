// Package envhost 统一解析 OpenClaw 各云端接口的环境主机。
//
// 环境名取自 PHONE_NOTIFICATIONS_ENV（development/test/production，缺省 production），
// 每个环境可用 OPENCLAW_HOST_{DEVELOPMENT,TEST,PRODUCTION} 覆盖默认域名。
// daemon Relay 隧道、灯效 API、ASR 接口共用此处逻辑，保证三套接口环境切换一致。
package envhost

import (
	"os"
	"regexp"
	"strings"
)

// defaultHosts 是各环境默认主机（构建期可通过 OPENCLAW_HOST_* 注入覆盖）。
var defaultHosts = map[string]string{
	"development": "openclaw-service-dev.yoooclaw.com",
	"test":        "openclaw-service-test.yoooclaw.com",
	"production":  "openclaw-service.yoooclaw.com",
}

var (
	schemeRE        = regexp.MustCompile(`^(https?|wss?)://`)
	trailingSlashRE = regexp.MustCompile(`/+$`)
)

// Normalize 去掉协议前缀、首尾空白与尾部斜杠，得到纯主机名。
func Normalize(host string) string {
	trimmed := strings.TrimSpace(host)
	if trimmed == "" {
		return ""
	}
	trimmed = schemeRE.ReplaceAllString(trimmed, "")
	return trailingSlashRE.ReplaceAllString(trimmed, "")
}

// Name 返回当前环境名（PHONE_NOTIFICATIONS_ENV 优先，未知值回退 production）。
func Name() string {
	switch env := strings.TrimSpace(os.Getenv("PHONE_NOTIFICATIONS_ENV")); env {
	case "development", "test", "production":
		return env
	default:
		return "production"
	}
}

// Host 返回当前环境主机：先看 OPENCLAW_HOST_* 覆盖，否则取默认域名。
func Host() string {
	env := Name()
	var override string
	switch env {
	case "development":
		override = os.Getenv("OPENCLAW_HOST_DEVELOPMENT")
	case "test":
		override = os.Getenv("OPENCLAW_HOST_TEST")
	case "production":
		override = os.Getenv("OPENCLAW_HOST_PRODUCTION")
	}
	if h := Normalize(override); h != "" {
		return h
	}
	return defaultHosts[env]
}

// IsDefaultHost 报告 host（归一化后）是否为某个环境的默认域名。
// 用于判断持久化的 URL 是否仍是内置默认值（而非用户自定义），从而决定可否随环境切换。
func IsDefaultHost(host string) bool {
	h := Normalize(host)
	if h == "" {
		return false
	}
	for _, def := range defaultHosts {
		if h == def {
			return true
		}
	}
	return false
}
