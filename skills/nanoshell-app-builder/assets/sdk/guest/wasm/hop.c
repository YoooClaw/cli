/* Hop — charge-jump (Micropixel jump-jump thin port).
 * Hold OK to charge, release to jump. BACK exits.
 * Web + device Draw path; digit HUD via rects (no libc).
 */
#include "ns_abi.h"

enum {
	W = 240,
	H = 120,
	GROUND_Y = 96,
	PLAT_H = 10,
	BALL_R = 6
};

enum { PH_READY = 0, PH_CHARGE, PH_FLY, PH_DEAD };

static int32_t abs_i(int32_t v)
{
	return v < 0 ? -v : v;
}

static void digit(int32_t x, int32_t y, int32_t d, int32_t c)
{
	/* 3x5 block digits 0-9 */
	static const uint8_t g[10] = {
		0x7B, /* 0 approx via segments as 5 rows packed loosely — use patterns */
		0x31, 0x75, 0x75, 0x3D, 0x6E, 0x6F, 0x71, 0x7F, 0x7E
	};
	/* Simple 3x5 bitmaps */
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
	(void)g;
	p = bm[d];
	for (row = 0; row < 5; row++) {
		for (col = 0; col < 3; col++) {
			if (p[row * 3 + col] == '1') {
				ns_fill_rect(x + col * 2, y + row * 2, 2, 2, c);
			}
		}
	}
}

static void draw_num(int32_t x, int32_t y, int32_t n, int32_t c)
{
	int32_t d[4];
	int32_t i = 0;
	int32_t v = n;

	if (v < 0) {
		v = 0;
	}
	if (v == 0) {
		digit(x, y, 0, c);
		return;
	}
	while (v > 0 && i < 4) {
		d[i++] = v % 10;
		v /= 10;
	}
	while (i > 0) {
		i--;
		digit(x, y, d[i], c);
		x += 8;
	}
}

static int32_t clampi(int32_t v, int32_t lo, int32_t hi)
{
	if (v < lo) {
		return lo;
	}
	if (v > hi) {
		return hi;
	}
	return v;
}

NS_WASM_EXPORT void start(void)
{
	int32_t phase = PH_READY;
	int32_t score = 0;
	int32_t best = 0;
	int32_t charge = 0;
	int32_t px = 40;
	int32_t py = GROUND_Y - BALL_R;
	int32_t vx = 0;
	int32_t vy = 0;
	int32_t cur_x = 20;
	int32_t cur_w = 50;
	int32_t next_x = 120;
	int32_t next_w = 44;
	int32_t holding = 0;
	uint32_t rng = 0xC0FFEEu;

	/* best via KV if available */
	{
		char buf[8];
		int32_t n = ns_kv_get("hop_best", buf, (int32_t)sizeof(buf));
		if (n > 0) {
			int32_t i;
			best = 0;
			for (i = 0; i < n && buf[i] >= '0' && buf[i] <= '9'; i++) {
				best = best * 10 + (buf[i] - '0');
			}
		}
	}

	while (!ns_should_exit()) {
		int32_t packed;

		ns_heartbeat();

		while ((packed = ns_event_poll()) != 0) {
			int32_t type = packed & 0xff;
			int32_t handle = (packed >> 8) & 0xff;
			int32_t value = (packed >> 16) & 0xffff;

			if (type == NS_EVT_STOP) {
				ns_exit();
				break;
			}
			if (type != NS_EVT_KEY) {
				continue;
			}
			if (value == NS_KEY_BACK) {
				ns_exit();
				break;
			}
			if (value == NS_KEY_OK) {
				if (handle == NS_KEY_PHASE_DOWN || handle == 0) {
					if (phase == PH_READY || phase == PH_DEAD) {
						if (phase == PH_DEAD) {
							score = 0;
							px = cur_x + cur_w / 2;
							py = GROUND_Y - BALL_R;
							phase = PH_READY;
						}
						holding = 1;
						charge = 0;
						phase = PH_CHARGE;
					}
				} else if (handle == NS_KEY_PHASE_UP ||
					   handle == NS_KEY_PHASE_REPEAT) {
					if (handle == NS_KEY_PHASE_REPEAT &&
					    phase == PH_CHARGE) {
						charge += 2;
						if (charge > 40) {
							charge = 40;
						}
					}
					if (handle == NS_KEY_PHASE_UP &&
					    phase == PH_CHARGE) {
						int32_t power =
							clampi(charge, 8, 40);
						holding = 0;
						vx = 2 + power / 4;
						vy = -(4 + power / 5);
						phase = PH_FLY;
						charge = 0;
					}
				}
			}
		}

		/* poll_key fallback: OK while charging counts as hold tick */
		{
			int32_t k = ns_poll_key();
			if (k == NS_KEY_BACK) {
				ns_exit();
				break;
			}
			if (phase == PH_CHARGE) {
				if (k == NS_KEY_OK || holding) {
					charge += 1;
					if (charge > 40) {
						charge = 40;
					}
				}
			} else if (phase == PH_READY && k == NS_KEY_OK) {
				holding = 1;
				charge = 0;
				phase = PH_CHARGE;
			} else if (phase == PH_DEAD && k == NS_KEY_OK) {
				score = 0;
				px = cur_x + cur_w / 2;
				py = GROUND_Y - BALL_R;
				phase = PH_READY;
			}
		}

		/* Auto-release if charged via poll-only path (no UP events). */
		if (phase == PH_CHARGE && charge >= 40) {
			int32_t power = 40;
			holding = 0;
			vx = 2 + power / 4;
			vy = -(4 + power / 5);
			phase = PH_FLY;
		}

		if (phase == PH_FLY) {
			px += vx;
			py += vy;
			vy += 1; /* gravity */
			if (py >= GROUND_Y - BALL_R) {
				py = GROUND_Y - BALL_R;
				/* land on next? */
				if (px >= next_x - 2 && px <= next_x + next_w + 2) {
					int32_t cx = next_x + next_w / 2;
					int32_t dist = abs_i(px - cx);
					score += 1;
					if (dist <= 4) {
						score += 1; /* center bonus */
					}
					if (score > best) {
						char b[8];
						int32_t t = score;
						int32_t n = 0;
						char tmp[8];
						int32_t i;
						best = score;
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
						ns_kv_set("hop_best", b);
					}
					cur_x = next_x;
					cur_w = next_w;
					px = cur_x + cur_w / 2;
					rng ^= rng << 13;
					rng ^= rng >> 17;
					rng ^= rng << 5;
					next_w = 28 + (int32_t)(rng % 28u);
					next_x = cur_x + cur_w + 20 +
						 (int32_t)(rng % 50u);
					if (next_x + next_w > W - 8) {
						/* wrap platforms toward left camera feel */
						cur_x = 24;
						next_x = 90 + (int32_t)(rng % 40u);
						px = cur_x + cur_w / 2;
					}
					phase = PH_READY;
					vx = 0;
					vy = 0;
					charge = 0;
				} else if (px < cur_x - 4 ||
					   px > cur_x + cur_w + 4) {
					phase = PH_DEAD;
					vx = 0;
					vy = 0;
				} else {
					phase = PH_READY;
					vx = 0;
					vy = 0;
					charge = 0;
				}
			}
		}

		ns_clear(NS_RGB565(12, 18, 32));
		/* ground */
		ns_fill_rect(0, GROUND_Y + 2, W, H - GROUND_Y, NS_RGB565(30, 40, 55));
		/* platforms */
		ns_fill_round_rect(cur_x, GROUND_Y - PLAT_H, cur_w, PLAT_H, 3,
				   NS_RGB565(70, 140, 200));
		ns_fill_round_rect(next_x, GROUND_Y - PLAT_H, next_w, PLAT_H, 3,
				   NS_RGB565(90, 180, 120));
		/* charge bar */
		if (phase == PH_CHARGE) {
			ns_fill_rect(8, 8, charge * 4, 6, NS_RGB565(240, 180, 40));
		}
		/* player */
		ns_draw_sprite(NS_SPRITE_BALL, px - 4, py - 4);
		/* HUD */
		draw_num(180, 4, score, NS_RGB565(230, 230, 240));
		draw_num(180, 16, best, NS_RGB565(140, 160, 180));
		if (phase == PH_DEAD) {
			ns_draw_text_id(2, 70, 50, NS_RGB565(255, 120, 100), 1);
		}
		ns_present();
		ns_delay_ms(33);
	}
}
