package light

import "github.com/YoooClaw/cli/internal/envhost"

// LightAPIURL 返回灯效云 API 端点（主机随 PHONE_NOTIFICATIONS_ENV 切换，见 internal/envhost）。
func LightAPIURL() string {
	return "https://" + envhost.Host() + "/api/message/tob/sendMessage"
}
