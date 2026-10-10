/* QuakeWatch — notify containing UTF-8「地震」→ vibe + red LED + red flash.
 * SD/wasm package only (not a firmware builtin).
 * Keep .data + C-stack tiny: device memoryLimit is 4KiB.
 * OK idle = test; OK alert = silence; BACK = exit.
 */
#include "ns_abi.h"

enum { NS_NOTIFY_MAX = 200 };

static uint8_t s_pkt[NS_NOTIFY_MAX];

/* UTF-8 地震 */
static const char k_eq[] = "\xE5\x9C\xB0\xE9\x9C\x87";

static int32_t bytes_has(const char *hay, int32_t hay_len, const char *nd,
			 int32_t nd_len)
{
	int32_t i, j;

	if (!hay || !nd || hay_len < nd_len || nd_len <= 0) {
		return 0;
	}
	for (i = 0; i <= hay_len - nd_len; i++) {
		int32_t ok = 1;

		for (j = 0; j < nd_len; j++) {
			if (hay[i + j] != nd[j]) {
				ok = 0;
				break;
			}
		}
		if (ok) {
			return 1;
		}
	}
	return 0;
}

static int32_t ascii_quake(const char *hay, int32_t hay_len)
{
	static const char q[] = "quake";
	int32_t i, j;

	if (hay_len < 5) {
		return 0;
	}
	for (i = 0; i <= hay_len - 5; i++) {
		int32_t ok = 1;

		for (j = 0; j < 5; j++) {
			char c = hay[i + j];

			if (c >= 'A' && c <= 'Z') {
				c = (char)(c - 'A' + 'a');
			}
			if (c != q[j]) {
				ok = 0;
				break;
			}
		}
		if (ok) {
			return 1;
		}
	}
	return 0;
}

static int32_t text_quake(const char *s, int32_t len)
{
	return bytes_has(s, len, k_eq, 6) || ascii_quake(s, len);
}

static int32_t pkt_quake(const uint8_t *pkt, int32_t n)
{
	uint8_t tl;
	uint8_t bl;

	if (!pkt || n < 3) {
		return 0;
	}
	tl = pkt[0];
	if (1 + tl >= n) {
		return 0;
	}
	bl = pkt[1 + tl];
	if (2 + tl + bl > n) {
		bl = (uint8_t)(n - (2 + tl));
	}
	return text_quake((const char *)&pkt[1], (int32_t)tl) ||
	       text_quake((const char *)&pkt[2 + tl], (int32_t)bl);
}

static void alarm_on(void)
{
	if (ns_cap_has(NS_CAP_HAPTIC)) {
		(void)ns_vibe_pulse(60, 60, 0);
	}
	if (ns_cap_has(NS_CAP_LED)) {
		(void)ns_led_effect(NS_LED_BLINK, 0xFF, 0x00FF0000, 255, 60, 60);
	}
}

static void alarm_off(void)
{
	if (ns_cap_has(NS_CAP_HAPTIC)) {
		(void)ns_vibe_stop();
	}
	if (ns_cap_has(NS_CAP_LED)) {
		(void)ns_led_off();
	}
}

NS_WASM_EXPORT void start(void)
{
	int32_t alerting = 0;
	int32_t flash_on = 1;
	int32_t flash_next = 0;
	int32_t has_n = ns_cap_has(NS_CAP_NOTIFY);

	while (!ns_should_exit()) {
		int32_t now = ns_now_ms();
		int32_t key;

		ns_heartbeat();

		if (has_n) {
			int32_t n = ns_notify_recv(s_pkt, NS_NOTIFY_MAX, 0);

			if (n > 2 && pkt_quake(s_pkt, n) && !alerting) {
				alerting = 1;
				flash_on = 1;
				flash_next = now;
				alarm_on();
			}
		}

		if (alerting) {
			if ((int32_t)(now - flash_next) >= 0) {
				flash_on = !flash_on;
				flash_next = now + 80;
			}
			if (flash_on) {
				ns_clear(NS_RGB565(255, 0, 0));
				ns_draw_text_id(10, 80, 48, NS_RGB565(255, 255, 255),
						2);
			} else {
				ns_clear(NS_RGB565(40, 0, 0));
				ns_draw_text_id(11, 70, 48, NS_RGB565(255, 80, 80),
						2);
			}
			ns_draw_text_id(12, 40, 100, NS_RGB565(255, 200, 200), 1);
		} else {
			ns_clear(NS_RGB565(12, 14, 20));
			ns_draw_text_id(13, 8, 16, NS_RGB565(240, 240, 240), 2);
			ns_draw_text_id(14, 8, 48, NS_RGB565(160, 170, 180), 1);
			ns_draw_text_id(15, 8, 100, NS_RGB565(120, 130, 140), 1);
		}
		ns_present();

		key = ns_poll_key();
		if (key == NS_KEY_OK) {
			if (alerting) {
				alerting = 0;
				alarm_off();
			} else {
				alerting = 1;
				flash_on = 1;
				flash_next = now;
				alarm_on();
			}
		} else if (key == NS_KEY_BACK) {
			alarm_off();
			ns_exit();
			break;
		}
		ns_delay_ms(alerting ? 16 : 33);
	}
	alarm_off();
}
