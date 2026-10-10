/* Shared-memory peripheral simulator. Loaded by the synchronous Wasm worker. */
self.createHardware = function (memory, delay, stopped) {
  let state;
  const emit = (name, args, result = 0) => {
    self.postMessage({type: 'hardware', name, args, result, at: performance.now()});
    return result;
  };
  const bytes = (ptr, len) => {
    if (!memory() || !Number.isInteger(len) || len <= 0 || ptr <= 0 || ptr + len > memory().buffer.byteLength) return null;
    return new Uint8Array(memory().buffer, ptr, len);
  };
  const receive = (kind, ptr, cap, timeout) => {
    const out = bytes(ptr, cap);
    if (!out || !state) return -22;
    const h = kind === 'ble' ? 3 : 6;
    const base = kind === 'ble' ? 16 : 16 + 4 * 201;
    const until = timeout < 0 ? Infinity : performance.now() + timeout;
    do {
      if (Atomics.compareExchange(state, h, 0, 1) === 0) {
        try {
          const r = Atomics.load(state, h + 2), w = Atomics.load(state, h + 1);
          if (r !== w) {
            const offset = base + (r % 4) * 201;
            const n = Math.min(cap, state[offset]);
            for (let i = 0; i < n; i++) out[i] = state[offset + 1 + i];
            Atomics.store(state, h + 2, r + 1);
            return emit(kind + '_recv', {hex: Array.from(out.subarray(0, n), b => b.toString(16).padStart(2, '0')).join('')}, n);
          }
        } finally { Atomics.store(state, h, 0); }
      }
      if (!timeout || stopped() || performance.now() >= until) return 0;
      delay(1);
    } while (true);
  };
  return {
    init(buffer) { state = new Int32Array(buffer); },
    reset() { emit('host_reset', {}); },
    imports: {
      sys_battery: () => state ? Atomics.load(state, 0) : 87,
      sys_charge: () => state ? Atomics.load(state, 1) : 0,
      led_effect(mode, mask, color, brightness, on_ms, off_ms) {
        if (mode < 0 || mode > 3) return emit('led_effect', {mode, mask, color, brightness, on_ms, off_ms}, -22);
        return emit('led_effect', {mode, mask: mask === 0 ? 255 : mask & 255, color: color & 0xffffff,
          brightness: Math.max(0, Math.min(255, brightness)), on_ms: Math.min(65535, Math.max(0, on_ms)) || 200,
          off_ms: Math.min(65535, Math.max(0, off_ms)) || 800});
      },
      led_off: () => emit('led_off', {}),
      vibe(pattern) { return emit('vibe', {pattern}, pattern === 0 || pattern === 1 ? 0 : -22); },
      vibe_pulse(on_ms, off_ms, repeat) {
        if (on_ms <= 0) return emit('vibe_pulse', {on_ms, off_ms, repeat}, -22);
        return emit('vibe_pulse', {on_ms: Math.min(65535, on_ms), off_ms: Math.min(65535, Math.max(0, off_ms)), repeat: Math.min(255, Math.max(0, repeat))});
      },
      vibe_stop: () => emit('vibe_stop', {}),
      ble_send(ptr, len) {
        const data = bytes(ptr, len);
        const hex = data && len <= 180 ? Array.from(data, b => b.toString(16).padStart(2, '0')).join('') : '';
        return emit('ble_send', {hex}, !data || len > 180 ? -22 : !state || !Atomics.load(state, 2) ? -107 : len);
      },
      ble_recv: (ptr, cap, timeout) => receive('ble', ptr, cap, timeout),
      notify_recv: (ptr, cap, timeout) => receive('notify', ptr, cap, timeout),
    },
  };
};
