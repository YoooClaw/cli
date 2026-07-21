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

// hostOverride 返回当前环境对应的 OPENCLAW_HOST_* 覆盖（已归一化，可能为空）。
func hostOverride() string {
	switch Name() {
	case "development":
		return Normalize(os.Getenv("OPENCLAW_HOST_DEVELOPMENT"))
	case "test":
		return Normalize(os.Getenv("OPENCLAW_HOST_TEST"))
	default:
		return Normalize(os.Getenv("OPENCLAW_HOST_PRODUCTION"))
	}
}

// Explicit 报告调用方是否用环境变量显式指定过环境/主机：PHONE_NOTIFICATIONS_ENV
// 被设成已知环境名，或当前环境的 OPENCLAW_HOST_* 覆盖非空。
//
// Name() 对「没设」和「显式设成 production」都返回 production，二者不可区分；
// 而主机解析要让显式环境变量压过配置文件，就必须区分这两种情况——没设时配置说了算，
// 设了才轮到环境变量。见 config.ResolveCloudHost。
func Explicit() bool {
	switch strings.TrimSpace(os.Getenv("PHONE_NOTIFICATIONS_ENV")) {
	case "development", "test", "production":
		return true
	}
	return hostOverride() != ""
}

// Host 返回当前环境主机：先看 OPENCLAW_HOST_* 覆盖，否则取默认域名。
func Host() string {
	if h := hostOverride(); h != "" {
		return h
	}
	return defaultHosts[Name()]
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
