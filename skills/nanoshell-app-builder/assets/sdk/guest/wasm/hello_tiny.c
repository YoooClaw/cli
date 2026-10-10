/* Tiny SD-installable demo — keep under 12 KiB wasm for AiNote. */
#include "ns_abi.h"

NS_WASM_EXPORT void start(void)
{
	while (!ns_should_exit()) {
		ns_heartbeat();
		ns_clear(NS_RGB565(18, 22, 28));
		ns_draw_text_id(0, 8, 40, NS_RGB565(240, 240, 240), 2);
		ns_present();

		{
			int32_t key = ns_poll_key();
			if (key == NS_KEY_BACK) {
				ns_exit();
				break;
			}
		}
		ns_delay_ms(33);
	}
}
