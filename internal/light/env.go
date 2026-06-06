package light

import (
	"os"
	"regexp"
	"strings"
)

// 默认环境主机（构建期可通过 OPENCLAW_HOST_* 注入覆盖）。
var defaultEnvHosts = map[string]string{
	"development": "openclaw-service-dev.yoooclaw.com",
	"test":        "openclaw-service-test.yoooclaw.com",
	"production":  "openclaw-service.yoooclaw.com",
}

var schemeRE = regexp.MustCompile(`^(https?|wss?)://`)
var trailingSlashRE = regexp.MustCompile(`/+$`)

func normalizeHost(host string) string {
	trimmed := strings.TrimSpace(host)
	if trimmed == "" {
		return ""
	}
	trimmed = schemeRE.ReplaceAllString(trimmed, "")
	return trailingSlashRE.ReplaceAllString(trimmed, "")
}

// loadEnvName 解析当前环境名（PHONE_NOTIFICATIONS_ENV 环境变量优先，否则 production）。
func loadEnvName() string {
	env := strings.TrimSpace(os.Getenv("PHONE_NOTIFICATIONS_ENV"))
	switch env {
	case "development", "test", "production":
		return env
	}
	return "production"
}

func envHost() string {
	env := loadEnvName()
	var override string
	switch env {
	case "development":
		override = os.Getenv("OPENCLAW_HOST_DEVELOPMENT")
	case "test":
		override = os.Getenv("OPENCLAW_HOST_TEST")
	case "production":
		override = os.Getenv("OPENCLAW_HOST_PRODUCTION")
	}
	if h := normalizeHost(override); h != "" {
		return h
	}
	return defaultEnvHosts[env]
}

// LightAPIURL 返回灯效云 API 端点。
func LightAPIURL() string {
	return "https://" + envHost() + "/api/message/tob/sendMessage"
}
