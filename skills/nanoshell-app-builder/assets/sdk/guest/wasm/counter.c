/* Counter — Widget-style + KV best (WAMR increase thin port).
 * Web Host draws with soft rects (ui stubs don't paint canvas).
 * OK = +1, RIGHT = -1, BACK = exit. Best persisted in KV.
 */
#include "ns_abi.h"

static void digit(int32_t x, int32_t y, int32_t d, int32_t c, int32_t s)
{
	static const char *bm[10] = {
		"111101101101111", "010010010010010", "111001111100111",
		"111001111001111", "101101111001001", "111100111001111",
		"111100111101111", "111001001001001", "111101111101111",
		"111101111001111"
	};
	int32_t row, col;
	const char *p;

	if (d < 0 || d > 9) {
		return;
	}
	p = bm[d];
	for (row = 0; row < 5; row++) {
		for (col = 0; col < 3; col++) {
			if (p[row * 3 + col] == '1') {
				ns_fill_rect(x + col * s, y + row * s, s, s, c);
			}
		}
	}
}

static void draw_num(int32_t x, int32_t y, int32_t n, int32_t c, int32_t s)
{
	int32_t d[6];
	int32_t i = 0;
	int32_t v = n;
	int32_t neg = 0;

	if (v < 0) {
		neg = 1;
		v = -v;
	}
	if (v == 0) {
		digit(x, y, 0, c, s);
		return;
	}
	while (v > 0 && i < 6) {
		d[i++] = v % 10;
		v /= 10;
	}
	if (neg) {
		ns_fill_rect(x, y + 2 * s, 3 * s, s, c);
		x += 4 * s;
	}
	while (i > 0) {
		i--;
		digit(x, y, d[i], c, s);
		x += 4 * s;
	}
}

static void save_best(int32_t best)
{
	char b[8];
	char tmp[8];
	int32_t t = best;
	int32_t n = 0;
	int32_t i;

	if (t < 0) {
		t = 0;
	}
	if (t == 0) {
		b[0] = '0';
		b[1] = 0;
	} else {
		while (t > 0 && n < 7) {
			tmp[n++] = (char)('0' + (t % 10));
			t /= 10;
		}
		for (i = 0; i < n; i++) {
			b[i] = tmp[n - 1 - i];
		}
		b[n] = 0;
	}
	ns_kv_set("ctr_best", b);
}

NS_WASM_EXPORT void start(void)
{
	int32_t count = 0;
	int32_t best = 0;
	int32_t i;
	int32_t btn = 0; /* flash */

	{
		char buf[8];
		int32_t n = ns_kv_get("ctr_best", buf, (int32_t)sizeof(buf));
		if (n > 0) {
			for (i = 0; i < n && buf[i] >= '0' && buf[i] <= '9'; i++) {
				best = best * 10 + (buf[i] - '0');
			}
		}
	}

	/* Host widgets when available (device LVGL); web paints below. */
	(void)ns_ui_label_create(8, 8, 100, 16);
	(void)ns_ui_button_create(70, 80, 100, 28);
	ns_ui_set_text_id(1, 1);
	ns_ui_set_text_id(2, 4);

	while (!ns_should_exit()) {
		int32_t packed;
		int32_t key;

		ns_heartbeat();
		while ((packed = ns_event_poll()) != 0) {
			int32_t type = packed & 0xff;
			int32_t handle = (packed >> 8) & 0xff;
			int32_t value = (packed >> 16) & 0xffff;

			if (type == NS_EVT_STOP) {
				ns_exit();
				break;
			}
			if (type != NS_EVT_KEY || handle == NS_KEY_PHASE_UP) {
				continue;
			}
			if (value == NS_KEY_BACK) {
				ns_exit();
				break;
			}
			if (value == NS_KEY_OK) {
				count++;
				btn = 4;
				if (count > best) {
					best = count;
					save_best(best);
				}
			} else if (value == NS_KEY_RIGHT) {
				count--;
				btn = 4;
			}
		}

		key = ns_poll_key();
		if (key == NS_KEY_BACK) {
			ns_exit();
			break;
		}
		if (key == NS_KEY_OK) {
			count++;
			btn = 4;
			if (count > best) {
				best = count;
				save_best(best);
			}
		} else if (key == NS_KEY_RIGHT) {
			count--;
			btn = 4;
		}

		ns_clear(NS_RGB565(18, 22, 30));
		ns_fill_round_rect(16, 20, 208, 56, 6, NS_RGB565(36, 48, 64));
		draw_num(40, 32, count, NS_RGB565(240, 245, 255), 3);
		draw_num(16, 4, best, NS_RGB565(140, 170, 140), 1);
		{
			int32_t bc = btn ? NS_RGB565(70, 140, 220)
					 : NS_RGB565(50, 90, 150);
			ns_fill_round_rect(70, 84, 100, 26, 4, bc);
			ns_draw_text_id(4, 92, 90, NS_RGB565(240, 240, 240), 1);
		}
		if (btn > 0) {
			btn--;
		}
		ns_present();
		ns_delay_ms(33);
	}
}
