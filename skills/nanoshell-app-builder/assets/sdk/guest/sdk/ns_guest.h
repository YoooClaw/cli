/* NanoShell guest ABI — one source, two backends.
 *
 *   Wasm:   clang --target=wasm32  (default)  → import module "ns", export start
 *   Native: -DNS_GUEST_NATIVE=1               → wrappers around nanoshell_api_t
 *
 * Guest code always calls ns_* / NS_GUEST_EXPORT start().
 * Builtin wiring: NS_GUEST_BUILTIN(chrono) → guest_app_chrono().
 */
#ifndef NS_GUEST_H
#define NS_GUEST_H

#include <stdint.h>

#if defined(NS_GUEST_NATIVE) && NS_GUEST_NATIVE
#  define NS_GUEST_IS_NATIVE 1
#else
#  define NS_GUEST_IS_NATIVE 0
#endif

#if NS_GUEST_IS_NATIVE
#  include "nanoshell/api.h"
void ns_guest_bind(const nanoshell_api_t *api);
/* Per-TU entry — avoids colliding `start` when several apps link into firmware. */
#  define NS_GUEST_EXPORT static
#else
#  ifndef NS_WASM_IMPORT
#    if defined(__clang__)
#      define NS_WASM_IMPORT(name) \
	__attribute__((import_module("ns"), import_name(name)))
#      define NS_WASM_EXPORT \
	__attribute__((export_name("start")))
#    else
#      define NS_WASM_IMPORT(name)
#      define NS_WASM_EXPORT
#    endif
#  endif
#  define NS_GUEST_EXPORT NS_WASM_EXPORT
#endif

/* ---- API surface (wasm imports, or native wrappers via macros) ---- */

#if NS_GUEST_IS_NATIVE

void ns_guest_heartbeat(void);
int32_t ns_guest_should_exit(void);
void ns_guest_exit(void);
void ns_guest_clear(int32_t color);
void ns_guest_present(void);
void ns_guest_delay_ms(int32_t ms);
int32_t ns_guest_poll_key(void);
void ns_guest_fill_rect(int32_t x, int32_t y, int32_t w, int32_t h,
			int32_t color);
void ns_guest_fill_round_rect(int32_t x, int32_t y, int32_t w, int32_t h,
			      int32_t radius, int32_t color);
void ns_guest_fill_circle(int32_t cx, int32_t cy, int32_t r, int32_t color);
void ns_guest_draw_clear(int32_t color);
void ns_guest_draw_present(void);
void ns_guest_draw_rect(int32_t x, int32_t y, int32_t w, int32_t h,
			int32_t color);
void ns_guest_draw_round_rect(int32_t x, int32_t y, int32_t w, int32_t h,
			      int32_t radius, int32_t color);
void ns_guest_draw_line(int32_t x0, int32_t y0, int32_t x1, int32_t y1,
			int32_t color);
int32_t ns_guest_draw_sprite(int32_t sprite_id, int32_t x, int32_t y);
int32_t ns_guest_timer_create(int32_t delay_ms, int32_t period_ms);
int32_t ns_guest_timer_cancel(int32_t timer_id);
int32_t ns_guest_timer_restart(int32_t timer_id, int32_t interval_ms);
int32_t ns_guest_event_poll(void);
void ns_guest_draw_text_id(int32_t id, int32_t x, int32_t y, int32_t color,
			   int32_t scale);
void ns_guest_draw_text(const char *p, int32_t len, int32_t x, int32_t y,
			int32_t color, int32_t scale);
int32_t ns_guest_now_ms(void);
int32_t ns_guest_cap_has(int32_t id);
int32_t ns_guest_rec_get_state(void);
int32_t ns_guest_rec_is_starting(void);
int32_t ns_guest_rec_start(void);
int32_t ns_guest_rec_stop(void);
int32_t ns_guest_rec_pause(void);
int32_t ns_guest_rec_resume(void);
int32_t ns_guest_rec_elapsed_ms(void);
/* Copy path into buf; returns bytes excl NUL (like launch_arg_get). */
int32_t ns_guest_rec_filename(char *buf, int32_t cap);
int32_t ns_guest_rec_add_marker(const char *source);
int32_t ns_guest_screen_is_on(void);
int32_t ns_guest_screen_on(void);
int32_t ns_guest_screen_off(void);
int32_t ns_guest_screen_brightness_get(void);
int32_t ns_guest_screen_brightness_set(int32_t level);
int32_t ns_guest_sys_battery(void);
int32_t ns_guest_sys_charge(void);
int32_t ns_guest_ble_send(const void *p, int32_t len);
int32_t ns_guest_ble_recv(void *p, int32_t cap, int32_t timeout_ms);
int32_t ns_guest_led_effect(int32_t mode, int32_t mask, int32_t color,
			    int32_t bright, int32_t on_ms, int32_t off_ms);
int32_t ns_guest_led_off(void);
int32_t ns_guest_vibe(int32_t pattern);
int32_t ns_guest_vibe_pulse(int32_t on_ms, int32_t off_ms, int32_t repeat);
int32_t ns_guest_vibe_stop(void);
int32_t ns_guest_notify_recv(void *p, int32_t cap, int32_t timeout_ms);
int32_t ns_guest_ui_label_create(int32_t x, int32_t y, int32_t w, int32_t h);
int32_t ns_guest_ui_button_create(int32_t x, int32_t y, int32_t w, int32_t h);
int32_t ns_guest_ui_delete(int32_t handle);
int32_t ns_guest_ui_set_text_id(int32_t handle, int32_t text_id);
int32_t ns_guest_ui_set_pos(int32_t handle, int32_t x, int32_t y);
int32_t ns_guest_ui_set_size(int32_t handle, int32_t w, int32_t h);
int32_t ns_guest_ui_set_visible(int32_t handle, int32_t visible);
int32_t ns_guest_ui_set_fg(int32_t handle, int32_t color);
int32_t ns_guest_ui_set_bg(int32_t handle, int32_t color);
int32_t ns_guest_ui_hit_test(int32_t x, int32_t y);
int32_t ns_guest_ui_pointer(int32_t x, int32_t y, int32_t pressed);
int32_t ns_guest_asset_info(int32_t asset_id);
int32_t ns_guest_draw_text_font(int32_t font_id, int32_t text_id, int32_t x,
				int32_t y, int32_t color);
int32_t ns_guest_text_measure(int32_t font_id, int32_t text_id);
int32_t ns_guest_random_u32(void);
int32_t ns_guest_kv_set(const char *key, const char *val);
int32_t ns_guest_kv_get(const char *key, char *buf, int32_t cap);
int32_t ns_guest_kv_remove(const char *key);
int32_t ns_guest_launch_arg_count(void);
int32_t ns_guest_launch_arg_get(int32_t index, char *buf, int32_t cap);

#  define ns_heartbeat ns_guest_heartbeat
#  define ns_should_exit ns_guest_should_exit
#  define ns_exit ns_guest_exit
#  define ns_clear ns_guest_clear
#  define ns_present ns_guest_present
#  define ns_delay_ms ns_guest_delay_ms
#  define ns_poll_key ns_guest_poll_key
#  define ns_fill_rect ns_guest_fill_rect
#  define ns_fill_round_rect ns_guest_fill_round_rect
#  define ns_fill_circle ns_guest_fill_circle
#  define ns_draw_circle ns_guest_fill_circle
#  define ns_draw_clear ns_guest_draw_clear
#  define ns_draw_present ns_guest_draw_present
#  define ns_draw_rect ns_guest_draw_rect
#  define ns_draw_round_rect ns_guest_draw_round_rect
#  define ns_draw_line ns_guest_draw_line
#  define ns_draw_sprite ns_guest_draw_sprite
#  define ns_timer_create ns_guest_timer_create
#  define ns_timer_cancel ns_guest_timer_cancel
#  define ns_timer_restart ns_guest_timer_restart
#  define ns_event_poll ns_guest_event_poll
#  define ns_draw_text_id ns_guest_draw_text_id
#  define ns_draw_text ns_guest_draw_text
#  define ns_now_ms ns_guest_now_ms
#  define ns_cap_has ns_guest_cap_has
#  define ns_rec_get_state ns_guest_rec_get_state
#  define ns_rec_is_starting ns_guest_rec_is_starting
#  define ns_rec_start ns_guest_rec_start
#  define ns_rec_stop ns_guest_rec_stop
#  define ns_rec_pause ns_guest_rec_pause
#  define ns_rec_resume ns_guest_rec_resume
#  define ns_rec_elapsed_ms ns_guest_rec_elapsed_ms
#  define ns_rec_filename ns_guest_rec_filename
#  define ns_rec_add_marker ns_guest_rec_add_marker
#  define ns_screen_is_on ns_guest_screen_is_on
#  define ns_screen_on ns_guest_screen_on
#  define ns_screen_off ns_guest_screen_off
#  define ns_screen_brightness_get ns_guest_screen_brightness_get
#  define ns_screen_brightness_set ns_guest_screen_brightness_set
#  define ns_sys_battery ns_guest_sys_battery
#  define ns_sys_charge ns_guest_sys_charge
#  define ns_ble_send ns_guest_ble_send
#  define ns_ble_recv ns_guest_ble_recv
#  define ns_led_effect ns_guest_led_effect
#  define ns_led_off ns_guest_led_off
#  define ns_vibe ns_guest_vibe
#  define ns_vibe_pulse ns_guest_vibe_pulse
#  define ns_vibe_stop ns_guest_vibe_stop
#  define ns_notify_recv ns_guest_notify_recv
#  define ns_ui_label_create ns_guest_ui_label_create
#  define ns_ui_button_create ns_guest_ui_button_create
#  define ns_ui_delete ns_guest_ui_delete
#  define ns_ui_set_text_id ns_guest_ui_set_text_id
#  define ns_ui_set_pos ns_guest_ui_set_pos
#  define ns_ui_set_size ns_guest_ui_set_size
#  define ns_ui_set_visible ns_guest_ui_set_visible
#  define ns_ui_set_fg ns_guest_ui_set_fg
#  define ns_ui_set_bg ns_guest_ui_set_bg
#  define ns_ui_hit_test ns_guest_ui_hit_test
#  define ns_ui_pointer ns_guest_ui_pointer
#  define ns_asset_info ns_guest_asset_info
#  define ns_draw_text_font ns_guest_draw_text_font
#  define ns_text_measure ns_guest_text_measure
#  define ns_random_u32 ns_guest_random_u32
#  define ns_kv_set ns_guest_kv_set
#  define ns_kv_get ns_guest_kv_get
#  define ns_kv_remove ns_guest_kv_remove
#  define ns_launch_arg_count ns_guest_launch_arg_count
#  define ns_launch_arg_get ns_guest_launch_arg_get

#else /* wasm */

NS_WASM_IMPORT("heartbeat") void ns_heartbeat(void);
NS_WASM_IMPORT("should_exit") int32_t ns_should_exit(void);
NS_WASM_IMPORT("exit") void ns_exit(void);
NS_WASM_IMPORT("clear") void ns_clear(int32_t color);
NS_WASM_IMPORT("present") void ns_present(void);
NS_WASM_IMPORT("delay_ms") void ns_delay_ms(int32_t ms);
NS_WASM_IMPORT("poll_key") int32_t ns_poll_key(void);
NS_WASM_IMPORT("fill_rect") void ns_fill_rect(int32_t x, int32_t y, int32_t w,
					      int32_t h, int32_t color);
NS_WASM_IMPORT("fill_round_rect") void ns_fill_round_rect(int32_t x, int32_t y,
							  int32_t w, int32_t h,
							  int32_t radius,
							  int32_t color);
NS_WASM_IMPORT("fill_circle") void ns_fill_circle(int32_t cx, int32_t cy,
						  int32_t r, int32_t color);
NS_WASM_IMPORT("draw_circle") void ns_draw_circle(int32_t cx, int32_t cy,
						  int32_t r, int32_t color);
NS_WASM_IMPORT("draw_clear") void ns_draw_clear(int32_t color);
NS_WASM_IMPORT("draw_present") void ns_draw_present(void);
NS_WASM_IMPORT("draw_rect") void ns_draw_rect(int32_t x, int32_t y, int32_t w,
					      int32_t h, int32_t color);
NS_WASM_IMPORT("draw_round_rect") void ns_draw_round_rect(int32_t x, int32_t y,
							  int32_t w, int32_t h,
							  int32_t radius,
							  int32_t color);
NS_WASM_IMPORT("draw_line") void ns_draw_line(int32_t x0, int32_t y0, int32_t x1,
					      int32_t y1, int32_t color);
NS_WASM_IMPORT("draw_sprite") int32_t ns_draw_sprite(int32_t sprite_id,
						     int32_t x, int32_t y);
NS_WASM_IMPORT("timer_create") int32_t ns_timer_create(int32_t delay_ms,
						       int32_t period_ms);
NS_WASM_IMPORT("timer_cancel") int32_t ns_timer_cancel(int32_t timer_id);
NS_WASM_IMPORT("timer_restart") int32_t ns_timer_restart(int32_t timer_id,
							 int32_t interval_ms);
NS_WASM_IMPORT("event_poll") int32_t ns_event_poll(void);
NS_WASM_IMPORT("draw_text_id") void ns_draw_text_id(int32_t id, int32_t x,
						    int32_t y, int32_t color,
						    int32_t scale);
NS_WASM_IMPORT("draw_text") void ns_draw_text(const char *p, int32_t len,
					      int32_t x, int32_t y,
					      int32_t color, int32_t scale);
NS_WASM_IMPORT("now_ms") int32_t ns_now_ms(void);
NS_WASM_IMPORT("cap_has") int32_t ns_cap_has(int32_t id);
NS_WASM_IMPORT("rec_get_state") int32_t ns_rec_get_state(void);
NS_WASM_IMPORT("rec_is_starting") int32_t ns_rec_is_starting(void);
NS_WASM_IMPORT("rec_start") int32_t ns_rec_start(void);
NS_WASM_IMPORT("rec_stop") int32_t ns_rec_stop(void);
NS_WASM_IMPORT("rec_pause") int32_t ns_rec_pause(void);
NS_WASM_IMPORT("rec_resume") int32_t ns_rec_resume(void);
NS_WASM_IMPORT("rec_elapsed_ms") int32_t ns_rec_elapsed_ms(void);
NS_WASM_IMPORT("rec_filename") int32_t ns_rec_filename(char *buf, int32_t cap);
NS_WASM_IMPORT("rec_add_marker") int32_t ns_rec_add_marker(const char *source);
NS_WASM_IMPORT("screen_is_on") int32_t ns_screen_is_on(void);
NS_WASM_IMPORT("screen_on") int32_t ns_screen_on(void);
NS_WASM_IMPORT("screen_off") int32_t ns_screen_off(void);
NS_WASM_IMPORT("screen_brightness_get") int32_t ns_screen_brightness_get(void);
NS_WASM_IMPORT("screen_brightness_set") int32_t ns_screen_brightness_set(
	int32_t level);
NS_WASM_IMPORT("sys_battery") int32_t ns_sys_battery(void);
NS_WASM_IMPORT("sys_charge") int32_t ns_sys_charge(void);
NS_WASM_IMPORT("ble_send") int32_t ns_ble_send(const void *p, int32_t len);
NS_WASM_IMPORT("ble_recv") int32_t ns_ble_recv(void *p, int32_t cap,
					       int32_t timeout_ms);
NS_WASM_IMPORT("led_effect") int32_t ns_led_effect(int32_t mode, int32_t mask,
						   int32_t color, int32_t bright,
						   int32_t on_ms, int32_t off_ms);
NS_WASM_IMPORT("led_off") int32_t ns_led_off(void);
NS_WASM_IMPORT("vibe") int32_t ns_vibe(int32_t pattern);
NS_WASM_IMPORT("vibe_pulse") int32_t ns_vibe_pulse(int32_t on_ms, int32_t off_ms,
						   int32_t repeat);
NS_WASM_IMPORT("vibe_stop") int32_t ns_vibe_stop(void);
NS_WASM_IMPORT("notify_recv") int32_t ns_notify_recv(void *p, int32_t cap,
						     int32_t timeout_ms);
NS_WASM_IMPORT("ui_label_create") int32_t ns_ui_label_create(int32_t x, int32_t y,
							     int32_t w, int32_t h);
NS_WASM_IMPORT("ui_button_create") int32_t ns_ui_button_create(int32_t x,
							       int32_t y,
							       int32_t w,
							       int32_t h);
NS_WASM_IMPORT("ui_delete") int32_t ns_ui_delete(int32_t handle);
NS_WASM_IMPORT("ui_set_text_id") int32_t ns_ui_set_text_id(int32_t handle,
							   int32_t text_id);
NS_WASM_IMPORT("ui_set_pos") int32_t ns_ui_set_pos(int32_t handle, int32_t x,
						   int32_t y);
NS_WASM_IMPORT("ui_set_size") int32_t ns_ui_set_size(int32_t handle, int32_t w,
						     int32_t h);
NS_WASM_IMPORT("ui_set_visible") int32_t ns_ui_set_visible(int32_t handle,
							   int32_t visible);
NS_WASM_IMPORT("ui_set_fg") int32_t ns_ui_set_fg(int32_t handle, int32_t color);
NS_WASM_IMPORT("ui_set_bg") int32_t ns_ui_set_bg(int32_t handle, int32_t color);
NS_WASM_IMPORT("ui_hit_test") int32_t ns_ui_hit_test(int32_t x, int32_t y);
NS_WASM_IMPORT("ui_pointer") int32_t ns_ui_pointer(int32_t x, int32_t y,
						   int32_t pressed);
NS_WASM_IMPORT("asset_info") int32_t ns_asset_info(int32_t asset_id);
NS_WASM_IMPORT("draw_text_font") int32_t ns_draw_text_font(int32_t font_id,
							    int32_t text_id,
							    int32_t x, int32_t y,
							    int32_t color);
NS_WASM_IMPORT("text_measure") int32_t ns_text_measure(int32_t font_id,
						       int32_t text_id);
NS_WASM_IMPORT("random_u32") int32_t ns_random_u32(void);
NS_WASM_IMPORT("kv_set") int32_t ns_kv_set(const char *key, const char *val);
NS_WASM_IMPORT("kv_get") int32_t ns_kv_get(const char *key, char *buf,
					   int32_t cap);
NS_WASM_IMPORT("kv_remove") int32_t ns_kv_remove(const char *key);
NS_WASM_IMPORT("launch_arg_count") int32_t ns_launch_arg_count(void);
NS_WASM_IMPORT("launch_arg_get") int32_t ns_launch_arg_get(int32_t index,
							   char *buf,
							   int32_t cap);

#endif /* NS_GUEST_IS_NATIVE */

/* Firmware builtin: guest_app_<name> → bind + start(). No-op on wasm builds. */
#if NS_GUEST_IS_NATIVE
#  define NS_GUEST_BUILTIN(name)                                       \
	void guest_app_##name(const nanoshell_api_t *api)              \
	{                                                              \
		ns_guest_bind(api);                                    \
		start(); /* static NS_GUEST_EXPORT in this TU */       \
	}
#else
#  define NS_GUEST_BUILTIN(name) /* wasm: export start() only */
#endif

#ifndef NS_RGB565
#  define NS_RGB565(r, g, b) \
	((int32_t)(((((r) & 0xf8) << 8) | (((g) & 0xfc) << 3) | ((b) >> 3))))
#endif

#ifndef NS_KEY_NONE
#  define NS_KEY_NONE 0
#  define NS_KEY_LEFT 1
#  define NS_KEY_RIGHT 2
#  define NS_KEY_UP 3
#  define NS_KEY_DOWN 4
#  define NS_KEY_OK 5
#  define NS_KEY_BACK 6
#  define NS_KEY_INSTALL 7
#  define NS_KEY_UNINSTALL 8
#endif

#ifndef NS_CAP_GFX
#  define NS_CAP_GFX 1
#  define NS_CAP_INPUT 2
#  define NS_CAP_RECORDER 3
#  define NS_CAP_SYS_BATTERY 10
#  define NS_CAP_SYS_CHARGE 11
#  define NS_CAP_SYS_TIME 12
#  define NS_CAP_BLE_DATA 20
#  define NS_CAP_LED 21
#  define NS_CAP_HAPTIC 22
#  define NS_CAP_NOTIFY 23
#  define NS_CAP_UI 24
#  define NS_CAP_TIMER 25
#  define NS_CAP_KV 27
#  define NS_CAP_SCREEN 28
#endif

#ifndef NS_SPRITE_BALL
#  define NS_SPRITE_BALL 1
#  define NS_SPRITE_PLAYER 2
#  define NS_SPRITE_ENEMY 3
#  define NS_SPRITE_COIN 4
#endif

#ifndef NS_FONT_SMALL
#  define NS_FONT_SMALL 16
#  define NS_FONT_MEDIUM 17
#  define NS_FONT_LARGE 18
#endif

#ifndef NS_OK
#  define NS_OK 0
#  define NS_ERR_INVALID (-1)
#  define NS_ERR_NOT_FOUND (-2)
#  define NS_ERR_RESOURCE_EXHAUSTED (-3)
#  define NS_ERR_WOULD_BLOCK (-4)
#  define NS_ERR_NOT_SUPPORTED (-5)
#  define NS_ERR_VERSION_MISMATCH (-6)
#  define NS_ERR_BUSY (-7)
#  define NS_ERR_IO (-8)
#endif

#ifndef NS_EVT_KEY
#  define NS_EVT_KEY 1
#  define NS_EVT_TIMER 2
#  define NS_EVT_UI 3
#  define NS_EVT_STOP 4
#endif

#ifndef NS_KEY_PHASE_DOWN
#  define NS_KEY_PHASE_DOWN 1
#  define NS_KEY_PHASE_UP 2
#  define NS_KEY_PHASE_REPEAT 3
#endif

#ifndef NS_UI_EVT_CLICK
#  define NS_UI_EVT_CLICK 1
#  define NS_UI_EVT_MOVE 2
#endif

#ifndef NS_LED_OFF
#  define NS_LED_OFF 0
#  define NS_LED_STEADY 1
#  define NS_LED_BLINK 2
#  define NS_LED_BREATH 3
#endif

#ifndef NS_VIBE_NOTIFY
#  define NS_VIBE_NOTIFY 0
#  define NS_VIBE_KEY 1
#endif

#endif /* NS_GUEST_H */
