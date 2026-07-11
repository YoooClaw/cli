package light

import "github.com/YoooClaw/cli/internal/envhost"

// LightAPIURL 返回 Notification Intelligence Service 的插件侧一次性亮灯端点。
func LightAPIURL() string {
	return envhost.NotificationIntelligenceLightEffectsSendURL()
}

// LegacyLightAPIURL 返回 OpenClaw plugin 用于兼容旧部署的 message-service 入口。
func LegacyLightAPIURL() string { return envhost.LegacyLightMessageServiceURL() }
