/* Snake — tiny grid (Micropixel snake thin port).
 * Device (OK/BACK): OK turn left + start; BACK exit.
 * Sim may also send RIGHT = turn right.
 * One source: wasm + native builtin (-DNS_GUEST_NATIVE).
 */
#include "ns_guest.h"

enum {
	GW = 20,
	GH = 10,
	CELL = 10,
	OX = 20,
	OY = 14,
	MAX_LEN = 48
};

enum { DIR_U = 0, DIR_R = 1, DIR_D = 2, DIR_L = 3 };

static int32_t body_x[MAX_LEN];
static int32_t body_y[MAX_LEN];
static int32_t len;
static int32_t dir;
static int32_t pend; /* -1 none, else DIR_* */
static int32_t food_x;
static int32_t food_y;
static int32_t score;
static int32_t best;
static int32_t alive;
static int32_t started;
static uint32_t rng = 0xA5A5u;

static uint32_t rnd(void)
{
	rng ^= rng << 13;
	rng ^= rng >> 17;
	rng ^= rng << 5;
	return rng;
}

static void place_food(void)
{
	int32_t tries = 0;
	for (;;) {
		int32_t i;
		int32_t ok = 1;
		food_x = (int32_t)(rnd() % (uint32_t)GW);
		food_y = (int32_t)(rnd() % (uint32_t)GH);
		for (i = 0; i < len; i++) {
			if (body_x[i] == food_x && body_y[i] == food_y) {
				ok = 0;
				break;
			}
		}
		if (ok || ++tries > 64) {
			break;
		}
	}
}

static void reset_game(void)
{
	len = 3;
	body_x[0] = 5;
	body_y[0] = 5;
	body_x[1] = 4;
	body_y[1] = 5;
	body_x[2] = 3;
	body_y[2] = 5;
	dir = DIR_R;
	pend = -1;
	score = 0;
	alive = 1;
	started = 1;
	place_food();
}

static void turn_left(void)
{
	pend = (dir + 3) & 3;
}

static void turn_right(void)
{
	pend = (dir + 1) & 3;
}

static void step(void)
{
	int32_t nx;
	int32_t ny;
	int32_t i;
	int32_t grow = 0;

	if (!alive || !started) {
		return;
	}
	if (pend >= 0) {
		/* no 180 */
		if (((pend + 2) & 3) != dir) {
			dir = pend;
		}
		pend = -1;
	}
	nx = body_x[0];
	ny = body_y[0];
	if (dir == DIR_U) {
		ny--;
	} else if (dir == DIR_R) {
		nx++;
	} else if (dir == DIR_D) {
		ny++;
	} else {
		nx--;
	}
	if (nx < 0 || ny < 0 || nx >= GW || ny >= GH) {
		alive = 0;
		return;
	}
	for (i = 0; i < len; i++) {
		if (body_x[i] == nx && body_y[i] == ny) {
			alive = 0;
			return;
		}
	}
	if (nx == food_x && ny == food_y) {
		grow = 1;
		score++;
		if (score > best) {
			char b[8];
			int32_t t = score;
			int32_t n = 0;
			char tmp[8];
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
			ns_kv_set("snk_best", b);
		}
		place_food();
	}
	for (i = len - 1; i > 0; i--) {
		body_x[i] = body_x[i - 1];
		body_y[i] = body_y[i - 1];
	}
	body_x[0] = nx;
	body_y[0] = ny;
	if (grow && len < MAX_LEN - 1) {
		body_x[len] = body_x[len - 1];
		body_y[len] = body_y[len - 1];
		len++;
	}
}

static void digit(int32_t x, int32_t y, int32_t d, int32_t c)
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
				ns_fill_rect(x + col, y + row, 1, 1, c);
			}
		}
	}
}

static void draw_num(int32_t x, int32_t y, int32_t n, int32_t c)
{
	int32_t d[4];
	int32_t i = 0;
	int32_t v = n < 0 ? 0 : n;

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
		x += 5;
	}
}

NS_GUEST_EXPORT void start(void)
{
	int32_t tick = 0;
	int32_t i;

	{
		char buf[8];
		int32_t n = ns_kv_get("snk_best", buf, (int32_t)sizeof(buf));
		best = 0;
		if (n > 0) {
			for (i = 0; i < n && buf[i] >= '0' && buf[i] <= '9'; i++) {
				best = best * 10 + (buf[i] - '0');
			}
		}
	}
	started = 0;
	alive = 0;

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
			if (type != NS_EVT_KEY) {
				continue;
			}
			if (handle == NS_KEY_PHASE_UP) {
				continue;
			}
			if (value == NS_KEY_BACK) {
				ns_exit();
				break;
			}
			if (!started || !alive) {
				if (value == NS_KEY_OK) {
					reset_game();
				}
				continue;
			}
			/* Device Guest: only OK + BACK. OK = turn left (relative). */
			if (value == NS_KEY_OK || value == NS_KEY_LEFT) {
				turn_left();
			} else if (value == NS_KEY_RIGHT) {
				turn_right();
			}
		}

		key = ns_poll_key();
		if (key == NS_KEY_BACK) {
			ns_exit();
			break;
		}
		if ((!started || !alive) && key == NS_KEY_OK) {
			reset_game();
		} else if (started && alive) {
			if (key == NS_KEY_OK || key == NS_KEY_LEFT) {
				turn_left();
			} else if (key == NS_KEY_RIGHT) {
				turn_right();
			}
		}

		tick++;
		if (started && alive && (tick % 6) == 0) {
			step();
		}

		ns_clear(NS_RGB565(10, 14, 22));
		ns_fill_rect(OX - 2, OY - 2, GW * CELL + 4, GH * CELL + 4,
			     NS_RGB565(28, 36, 48));
		for (i = 0; i < len; i++) {
			int32_t c = (i == 0) ? NS_RGB565(80, 220, 120)
					     : NS_RGB565(40, 160, 90);
			ns_fill_rect(OX + body_x[i] * CELL + 1,
				     OY + body_y[i] * CELL + 1, CELL - 2,
				     CELL - 2, c);
		}
		ns_draw_sprite(NS_SPRITE_COIN, OX + food_x * CELL + 1,
			       OY + food_y * CELL + 1);
		draw_num(4, 2, score, NS_RGB565(230, 230, 240));
		draw_num(4, 10, best, NS_RGB565(140, 150, 170));
		if (!started) {
			ns_draw_text_id(1, 70, 50, NS_RGB565(200, 210, 230), 1);
		} else if (!alive) {
			ns_draw_text_id(2, 80, 50, NS_RGB565(255, 120, 100), 1);
		}
		ns_present();
		ns_delay_ms(33);
	}
}

NS_GUEST_BUILTIN(snake)
