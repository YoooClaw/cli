package light

import "github.com/YoooClaw/cli/internal/envhost"

// LightAPIURL 返回灯效云 API 端点（主机随 PHONE_NOTIFICATIONS_ENV 切换，见 internal/envhost）。
// 一次性亮灯走 Notification Intelligence Service 的插件侧 Facade，
// 由服务端负责 LightSegment 校验、线协议编码与 message-service 调用。
func LightAPIURL() string {
	return "https://" + envhost.Host() + "/api/plugin/notification-intelligence/light-effects/send"
}
