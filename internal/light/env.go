package light

import "github.com/YoooClaw/cli/internal/envhost"

// LightAPIURL 返回灯效云 API 端点。host 由调用方从 config.ResolveCloudHost 解析后传入；
// 传空则回落到 PHONE_NOTIFICATIONS_ENV 的环境默认值（见 internal/envhost）。
// 一次性亮灯走 Notification Intelligence Service 的插件侧 Facade，
// 由服务端负责 LightSegment 校验、线协议编码与 message-service 调用。
func LightAPIURL(host string) string {
	resolved := envhost.Normalize(host)
	if resolved == "" {
		resolved = envhost.Host()
	}
	return "https://" + resolved + "/api/plugin/notification-intelligence/light-effects/send"
}
