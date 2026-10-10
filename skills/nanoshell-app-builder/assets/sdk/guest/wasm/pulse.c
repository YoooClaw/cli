/* Pulse — LED mood + notify divert.
 * OK: cycle LED mood / silence alert toast
 * BACK: exit
 * Notify while NS owns screen → vibe + orange blink + on-screen flash.
 * One source: wasm + native builtin (-DNS_GUEST_NATIVE).
 */
#include "ns_guest.h"

enum { MOOD_OFF = 0, MOOD_BLUE = 1, MOOD_GREEN = 2, MOOD_COUNT = 3 };

#define NS_NOTIFY_MAX 200

static void apply_mood(int32_t mood)
{
	if (!ns_cap_has(NS_CAP_LED)) {
		return;
	}
	if (mood == MOOD_BLUE) {
		(void)ns_led_effect(NS_LED_BREATH, 0xFF, 0x0033AAFF, 120, 0, 0);
	} else if (mood == MOOD_GREEN) {
		(void)ns_led_effect(NS_LED_STEADY, 0xFF, 0x0033FF88, 100, 0, 0);
	} else {
		(void)ns_led_off();
	}
}

NS_GUEST_EXPORT void start(void)
{
	int32_t mood = MOOD_BLUE;
	int32_t toast_until = 0;
	int32_t led_restore_at = 0;
	int32_t hits = 0;
	int32_t has_n = ns_cap_has(NS_CAP_NOTIFY);
	int32_t has_l = ns_cap_has(NS_CAP_LED);
	int32_t has_v = ns_cap_has(NS_CAP_HAPTIC);

	apply_mood(mood);

	while (!ns_should_exit()) {
		int32_t now = ns_now_ms();
		int32_t key;
		int32_t alerting;

		ns_heartbeat();

		if (has_n) {
			uint8_t pkt[NS_NOTIFY_MAX];
			int32_t n = ns_notify_recv(pkt, NS_NOTIFY_MAX, 0);

			if (n > 2 && pkt[0] > 0 && (1 + pkt[0]) < n) {
				hits++;
				toast_until = now + 4500;
				if (has_v) {
					(void)ns_vibe(NS_VIBE_NOTIFY);
				}
				if (has_l) {
					(void)ns_led_effect(NS_LED_BLINK, 0xFF,
							    0x00FF8800, 255, 80,
							    120);
					led_restore_at = now + 1600;
				}
			}
		}

		if (led_restore_at && (int32_t)(now - led_restore_at) >= 0) {
			led_restore_at = 0;
			apply_mood(mood);
		}

		alerting = toast_until && (int32_t)(now - toast_until) < 0;
		if (alerting) {
			ns_clear(NS_RGB565(40, 20, 0));
			ns_draw_text("NOTIFY", 6, 70, 40, NS_RGB565(255, 180, 80),
				     2);
			ns_draw_text("OK dismiss", 10, 70, 80,
				     NS_RGB565(200, 160, 120), 1);
		} else {
			ns_clear(NS_RGB565(16, 20, 28));
			ns_draw_text("PULSE", 5, 12, 10, NS_RGB565(240, 240, 240),
				     2);
			ns_draw_text("OK = LED mood", 13, 12, 40,
				     NS_RGB565(140, 150, 160), 1);
			ns_draw_text("notify -> vibe+LED", 18, 12, 56,
				     NS_RGB565(120, 130, 140), 1);
			{
				char line[20];
				int32_t i = 0;
				int32_t h = hits;

				line[i++] = 'h';
				line[i++] = ':';
				if (h <= 0) {
					line[i++] = '0';
				} else {
					char tmp[8];
					int32_t n = 0;
					int32_t t = h;
					int32_t j;

					while (t > 0 && n < 6) {
						tmp[n++] = (char)('0' + (t % 10));
						t /= 10;
					}
					for (j = n - 1; j >= 0; j--) {
						line[i++] = tmp[j];
					}
				}
				line[i++] = ' ';
				line[i++] = 'm';
				line[i++] = (char)('0' + mood);
				ns_draw_text(line, i, 12, 76,
					     NS_RGB565(100, 110, 120), 1);
			}
			ns_draw_text("BACK exit", 9, 12, 100,
				     NS_RGB565(100, 110, 120), 1);
		}
		ns_present();

		key = ns_poll_key();
		if (key == NS_KEY_OK) {
			if (alerting) {
				toast_until = 0;
				led_restore_at = 0;
				apply_mood(mood);
			} else {
				mood = (mood + 1) % MOOD_COUNT;
				apply_mood(mood);
			}
		} else if (key == NS_KEY_BACK) {
			if (has_l) {
				(void)ns_led_off();
			}
			ns_exit();
			break;
		}
		ns_delay_ms(33);
	}
	if (has_l) {
		(void)ns_led_off();
	}
}

NS_GUEST_BUILTIN(pulse)
