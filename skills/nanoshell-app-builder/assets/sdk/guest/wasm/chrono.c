/* Chrono — stopwatch. OK start/stop, RIGHT reset when stopped, BACK exit.
 * One source: wasm (pack-nsp) + native builtin (-DNS_GUEST_NATIVE).
 */
#include "ns_guest.h"

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

static void draw_clock(int32_t x, int32_t y, int32_t ms, int32_t c, int32_t s)
{
	int32_t cent = (ms / 10) % 100;
	int32_t sec = (ms / 1000) % 60;
	int32_t min = (ms / 60000) % 100;
	int32_t gap = 4 * s;

	digit(x, y, (min / 10) % 10, c, s);
	digit(x + gap, y, min % 10, c, s);
	ns_fill_rect(x + 2 * gap + s / 2, y + s, s, s, c);
	ns_fill_rect(x + 2 * gap + s / 2, y + 3 * s, s, s, c);
	digit(x + 3 * gap, y, (sec / 10) % 10, c, s);
	digit(x + 4 * gap, y, sec % 10, c, s);
	ns_fill_rect(x + 5 * gap + s / 2, y + 4 * s, s, s, c);
	digit(x + 6 * gap, y, (cent / 10) % 10, c, s);
	digit(x + 7 * gap, y, cent % 10, c, s);
}

NS_GUEST_EXPORT void start(void)
{
	int32_t running = 0;
	int32_t base_ms = 0;
	int32_t acc_ms = 0;

	while (!ns_should_exit()) {
		int32_t shown;
		int32_t accent;
		int32_t key;

		ns_heartbeat();
		shown = acc_ms;
		if (running) {
			shown = acc_ms + (ns_now_ms() - base_ms);
		}
		accent = running ? NS_RGB565(56, 200, 140) : NS_RGB565(90, 140, 220);

		ns_clear(NS_RGB565(14, 18, 28));
		ns_fill_round_rect(6, 8, 228, 104, 8, NS_RGB565(28, 34, 48));
		ns_fill_rect(6, 8, 4, 104, accent);
		ns_draw_text("CHRONO", 6, 16, 14, NS_RGB565(160, 175, 195), 1);
		ns_fill_round_rect(186, 12, 40, 12, 3, accent);
		ns_draw_text(running ? "RUN " : "STOP", 4, 192, 14,
			     NS_RGB565(10, 14, 20), 1);
		draw_clock(24, 40, shown, NS_RGB565(245, 248, 255), 2);
		ns_draw_text("OK start/stop", 13, 16, 88, NS_RGB565(120, 135, 155),
			     1);
		ns_draw_text("RIGHT reset  BACK exit", 22, 16, 100,
			     NS_RGB565(100, 115, 135), 1);
		ns_present();

		key = ns_poll_key();
		if (key == NS_KEY_OK) {
			if (running) {
				acc_ms += ns_now_ms() - base_ms;
				running = 0;
			} else {
				base_ms = ns_now_ms();
				running = 1;
			}
		} else if ((key == NS_KEY_RIGHT || key == NS_KEY_LEFT) &&
			   !running) {
			acc_ms = 0;
		} else if (key == NS_KEY_BACK) {
			ns_exit();
			break;
		}
		ns_delay_ms(33);
	}
}

NS_GUEST_BUILTIN(chrono)
