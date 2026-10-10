importScripts("./hardware-worker.js");
/* NanoShell ns ABI host — runs inside Worker (keeps UI thread free). */

const W = 240;
const H = 120;
const NS_KEY_NONE = 0;

/** @type {OffscreenCanvas} */
let canvas;
/** @type {OffscreenCanvasRenderingContext2D} */
let ctx;
/** @type {number[]} */
let keyQueue = [];
let exitFlag = 0;
let running = false;

/** Shared with UI thread: [0]=write, [1]=read, [2..17]=packed keys */
/** @type {Int32Array | null} */
let keySab = null;
/** @type {Int32Array | null} */
let sleepSab = null;

/** @type {{ memory: WebAssembly.Memory | null }} */
const link = { memory: null };

const TEXT_IDS = {
  0: "WASM Hello",
  1: "OK start",
  2: "OK again",
  3: "Label",
  4: "Button",
  5: "Host API v1",
  10: "QUAKE!",
  11: "EQ ALERT",
  12: "OK silence",
  13: "QuakeWatch",
  14: "wait notify",
  15: "OK=test BACK",
};

/** @type {Map<string, string>} */
const kvNamespaces = new Map();
let kvStore = new Map();
let kvWrites = 0;
/* Fake recorder (NS_CAP_RECORDER) — mirrors plat_record_sim state machine. */
let recState = 0; /* 0 idle, 1 recording, 2 paused */
let recStartMs = 0;
let recAccumMs = 0;
const REC_FAKE_NAME = "web-preview.wav";
let screenOn = 1;
let screenBright = 200;
let screenBrightSaved = 200;

/** @type {{ due: number, period: number, id: number }[]} */
let timers = [];
/** @type {number[]} packed events */
let eventQ = [];

function rgb565ToCss(c) {
  const r = (((c >> 11) & 0x1f) * 255) / 31;
  const g = (((c >> 5) & 0x3f) * 255) / 63;
  const b = ((c & 0x1f) * 255) / 31;
  return `rgb(${r | 0},${g | 0},${b | 0})`;
}

function pushKey(packed) {
  if (keyQueue.length > 16) keyQueue.shift();
  keyQueue.push(packed | 0);
}

/** Pull keys written by the UI thread (works during busy delay_ms). */
function drainSharedKeys() {
  if (!keySab) return;
  let r = Atomics.load(keySab, 1);
  const w = Atomics.load(keySab, 0);
  while (r !== w) {
    const packed = Atomics.load(keySab, 2 + (r & 15));
    pushKey(packed);
    r = (r + 1) | 0;
    Atomics.store(keySab, 1, r);
  }
}

function busyDelay(ms) {
  const end = performance.now() + Math.max(0, ms | 0);
  while (performance.now() < end) {
    drainSharedKeys();
    tickTimers();
    if (sleepSab) {
      const left = end - performance.now();
      if (left <= 0) break;
      const slice = left > 5 ? 5 : Math.max(1, left | 0);
      const before = Atomics.load(sleepSab, 0);
      Atomics.wait(sleepSab, 0, before, slice);
    }
  }
  drainSharedKeys();
}

function pushEvt(type, handle, value) {
  const packed = (type & 0xff) | ((handle & 0xff) << 8) | ((value & 0xffff) << 16);
  const required = type === 2 || type === 4; /* TIMER / STOP */
  const isMove = type === 3 && (value & 0xffff) === 2;
  if (type === 2) {
    for (let i = 0; i < eventQ.length; i++) {
      const t = eventQ[i] & 0xff;
      const h = (eventQ[i] >> 8) & 0xff;
      if (t === 2 && h === (handle & 0xff)) {
        let miss = ((eventQ[i] >> 16) & 0xffff) + (value & 0xffff);
        if (miss > 255) miss = 255;
        eventQ[i] = (2) | ((handle & 0xff) << 8) | ((miss & 0xffff) << 16);
        return;
      }
    }
  }
  if (isMove) {
    for (let i = eventQ.length - 1; i >= 0; i--) {
      const t = eventQ[i] & 0xff;
      const h = (eventQ[i] >> 8) & 0xff;
      const v = (eventQ[i] >> 16) & 0xffff;
      if (t === 3 && h === (handle & 0xff) && v === 2) {
        eventQ[i] = packed;
        return;
      }
    }
  }
  if (eventQ.length >= 16) {
    const idx = eventQ.findIndex((p) => {
      const t = p & 0xff;
      return t !== 2 && t !== 4;
    });
    if (idx >= 0) eventQ.splice(idx, 1);
    else if (!required) return;
    else return;
  }
  if (type === 1 && handle === 3) {
    for (let i = eventQ.length - 1; i >= 0; i--) {
      const t = eventQ[i] & 0xff;
      const h = (eventQ[i] >> 8) & 0xff;
      const v = (eventQ[i] >> 16) & 0xffff;
      if (t === 1 && h === 3 && v === (value & 0xffff)) {
        eventQ[i] = packed;
        return;
      }
    }
  }
  eventQ.push(packed);
}

function tickTimers() {
  const now = performance.now() | 0;
  for (const t of timers) {
    if (!t.due) continue;
    let missed = 0;
    if (now >= t.due) {
      if (t.period > 0) {
        while (now >= t.due && missed < 255) {
          t.due += t.period;
          missed++;
        }
      } else {
        t.due = 0;
        missed = 1;
      }
      pushEvt(2 /* TIMER */, t.id, missed);
    }
  }
}

function readCString(ptr, maxLen) {
  if (!link.memory || ptr == null) return "";
  const max = Math.max(1, maxLen | 0);
  const mem = new Uint8Array(link.memory.buffer, ptr >>> 0, max);
  let s = "";
  for (let i = 0; i < mem.length; i++) {
    if (mem[i] === 0) break;
    s += String.fromCharCode(mem[i]);
  }
  return s;
}

function writeCString(ptr, str, cap) {
  if (!link.memory || ptr == null || cap <= 0) return;
  const mem = new Uint8Array(link.memory.buffer, ptr >>> 0, cap | 0);
  const n = Math.min(String(str).length, (cap | 0) - 1);
  for (let i = 0; i < n; i++) mem[i] = String(str).charCodeAt(i) & 0xff;
  mem[n] = 0;
}

function ensureCanvas() {
  if (!canvas) {
    canvas = new OffscreenCanvas(W, H);
    ctx = canvas.getContext("2d");
    ctx.imageSmoothingEnabled = false;
  }
}

/** Strip optional NSP1 16-byte header. */
function unwrapNsp1(bytes) {
  const u8 = bytes instanceof Uint8Array ? bytes : new Uint8Array(bytes);
  if (
    u8.length >= 16 &&
    u8[0] === 0x4e &&
    u8[1] === 0x53 &&
    u8[2] === 0x50 &&
    u8[3] === 0x31
  ) {
    const wasmLen = u8[8] | (u8[9] << 8) | (u8[10] << 16) | (u8[11] << 24);
    if (wasmLen > 0 && u8.length >= 16 + wasmLen) {
      const minW = u8[12];
      const minH = u8[13];
      if (minW && W < minW) throw new Error("NSP1 min_w");
      if (minH && H < minH) throw new Error("NSP1 min_h");
      return u8.subarray(16, 16 + wasmLen);
    }
  }
  return u8;
}

function envMemCopy(dest, src, n, overlapOk) {
  const mem = link.memory;
  if (!mem || n <= 0) return dest;
  const buf = new Uint8Array(mem.buffer);
  const d = dest >>> 0;
  const s = src >>> 0;
  const len = n >>> 0;
  if (!overlapOk || d === s) {
    buf.copyWithin(d, s, s + len);
  } else if (d < s) {
    for (let i = 0; i < len; i++) buf[d + i] = buf[s + i];
  } else {
    for (let i = len - 1; i >= 0; i--) buf[d + i] = buf[s + i];
  }
  return dest;
}

function envMemSet(dest, c, n) {
  const mem = link.memory;
  if (!mem || n <= 0) return dest;
  const buf = new Uint8Array(mem.buffer);
  const d = dest >>> 0;
  const len = n >>> 0;
  const v = c & 0xff;
  for (let i = 0; i < len; i++) buf[d + i] = v;
  return dest;
}

const hardware = self.createHardware(() => link.memory, busyDelay, () => exitFlag);

function makeImports() {
  const draw = {
    clear(color) {
      ensureCanvas();
      ctx.fillStyle = rgb565ToCss(color);
      ctx.fillRect(0, 0, W, H);
    },
    present() {
      ensureCanvas();
      const bmp = canvas.transferToImageBitmap();
      self.postMessage({ type: "frame", bitmap: bmp }, [bmp]);
      canvas = null;
      ctx = null;
    },
    fill_rect(x, y, w, h, color) {
      ensureCanvas();
      ctx.fillStyle = rgb565ToCss(color);
      ctx.fillRect(x, y, w, h);
    },
    fill_round_rect(x, y, w, h, _r, color) {
      ensureCanvas();
      ctx.fillStyle = rgb565ToCss(color);
      ctx.fillRect(x, y, w, h);
    },
    draw_line(x0, y0, x1, y1, color) {
      ensureCanvas();
      ctx.strokeStyle = rgb565ToCss(color);
      ctx.beginPath();
      ctx.moveTo(x0, y0);
      ctx.lineTo(x1, y1);
      ctx.stroke();
    },
    draw_sprite(_id, x, y) {
      ensureCanvas();
      ctx.fillStyle = "#fc0";
      ctx.fillRect(x, y, 8, 8);
      return 0;
    },
    fill_circle(cx, cy, r, color) {
      ensureCanvas();
      ctx.fillStyle = rgb565ToCss(color);
      ctx.beginPath();
      ctx.arc(cx, cy, Math.max(0, r | 0), 0, Math.PI * 2);
      ctx.fill();
    },
    draw_text_id(id, x, y, color, scale) {
      ensureCanvas();
      const s = Math.max(1, scale | 0);
      ctx.fillStyle = rgb565ToCss(color);
      ctx.font = `${8 * s}px monospace`;
      ctx.textBaseline = "top";
      ctx.fillText(TEXT_IDS[id] || "?", x, y);
    },
    draw_text(ptr, len, x, y, color, scale) {
      ensureCanvas();
      let str = "?";
      if (link.memory && len > 0) {
        const mem = new Uint8Array(
          link.memory.buffer,
          ptr >>> 0,
          Math.min(len | 0, 256)
        );
        str = "";
        for (let i = 0; i < mem.length; i++) {
          if (mem[i] === 0) break;
          str += String.fromCharCode(mem[i]);
        }
      }
      const s = Math.max(1, scale | 0);
      ctx.fillStyle = rgb565ToCss(color);
      ctx.font = `${8 * s}px monospace`;
      ctx.textBaseline = "top";
      ctx.fillText(str, x, y);
    },
  };

  return {
    env: {
      // Fallback if an old/other wasm still imports libc bits
      memcpy(dest, src, n) {
        return envMemCopy(dest, src, n, false);
      },
      memmove(dest, src, n) {
        return envMemCopy(dest, src, n, true);
      },
      memset(dest, c, n) {
        return envMemSet(dest, c, n);
      },
      abort() {
        throw new Error("env.abort");
      },
    },
    ns: {
      heartbeat() {
        drainSharedKeys();
        tickTimers();
      },
      should_exit() {
        return exitFlag ? 1 : 0;
      },
      exit() {
        exitFlag = 1;
        pushEvt(4 /* STOP */, 0, 0);
      },
      delay_ms(ms) {
        tickTimers();
        busyDelay(ms);
      },
      poll_key() {
        drainSharedKeys();
        tickTimers();
        while (keyQueue.length) {
          const packed = keyQueue.shift();
          const key = packed & 0xff;
          const phase = (packed >> 8) & 0xff;
          /* DOWN only — UP(2)/REPEAT(3) via event_poll */
          if (phase !== 1 && phase !== 0) continue;
          return key;
        }
        return NS_KEY_NONE;
      },
      ...draw,
      draw_clear: draw.clear,
      draw_present: draw.present,
      draw_rect: draw.fill_rect,
      draw_round_rect: draw.fill_round_rect,
      draw_circle: draw.fill_circle,
      now_ms() {
        return performance.now() | 0;
      },
      timer_create(delay, period) {
        if (timers.length >= 6) return 0;
        const id = timers.length + 1;
        const now = performance.now() | 0;
        timers.push({
          id,
          due: now + Math.max(1, delay | 0),
          period: period | 0,
        });
        return id;
      },
      timer_cancel(id) {
        const t = timers.find((x) => x.id === (id | 0));
        if (!t) return -1;
        t.due = 0;
        return 0;
      },
      timer_restart(id, interval) {
        const t = timers.find((x) => x.id === (id | 0));
        if (!t || !t.due) return -2;
        const now = performance.now() | 0;
        if ((interval | 0) > 0) {
          t.period = interval | 0;
          t.due = now + t.period;
        } else {
          t.due = now + Math.max(1, t.period | 0);
        }
        return 0;
      },
      event_poll() {
        drainSharedKeys();
        tickTimers();
        while (keyQueue.length) {
          const packed = keyQueue.shift();
          const key = packed & 0xff;
          let phase = (packed >> 8) & 0xff;
          if (!phase) phase = 1;
          pushEvt(1 /* KEY */, phase, key);
        }
        if (!eventQ.length) return 0;
        return eventQ.shift();
      },
      ui_label_create() {
        return 1;
      },
      ui_button_create() {
        return 2;
      },
      ui_delete() {
        return 0;
      },
      ui_set_text_id() {
        return 0;
      },
      ui_set_pos() {
        return 0;
      },
      ui_set_size() {
        return 0;
      },
      ui_set_visible() {
        return 0;
      },
      ui_set_fg() {
        return 0;
      },
      ui_set_bg() {
        return 0;
      },
      ui_hit_test() {
        return 0;
      },
      ui_pointer() {
        return 0;
      },
      asset_info(id) {
        if (id >= 16 && id <= 18) {
          const sc = id - 15;
          return 2 | (sc << 8) | ((5 * sc) << 16) | ((7 * sc) << 24);
        }
        if (id >= 1 && id <= 4) {
          return 1 | (1 << 8) | (8 << 16) | (8 << 24);
        }
        return -2;
      },
      draw_text_font(_font, _tid, x, y, color) {
        ensureCanvas();
        ctx.fillStyle = rgb565ToCss(color);
        ctx.fillText("?", x, y + 8);
        return 0;
      },
      text_measure(_font, tid) {
        const s = TEXT_IDS[tid] || "";
        const w = s.length * 6;
        return w | (7 << 16);
      },
      random_u32() {
        return (Math.random() * 0xffffffff) >>> 0;
      },
      kv_set(keyPtr, valPtr) {
        const key = readCString(keyPtr, 16);
        const val = readCString(valPtr, 32);
        if (!key || key.length >= 16 || val.length >= 32) return -1;
        if (kvWrites >= 64) return -7;
        if (!kvStore.has(key) && kvStore.size >= 8) return -3;
        kvWrites++;
        kvStore.set(key, val);
        return 0;
      },
      kv_get(keyPtr, bufPtr, cap) {
        const key = readCString(keyPtr, 16);
        if (!key || !kvStore.has(key)) return -2;
        const val = kvStore.get(key) || "";
        if (!bufPtr || cap <= 0) return val.length;
        if (cap < val.length + 1) return -1;
        writeCString(bufPtr, val, cap);
        return val.length;
      },
      kv_remove(keyPtr) {
        const key = readCString(keyPtr, 16);
        if (!key || !kvStore.has(key)) return -2;
        kvStore.delete(key);
        return 0;
      },
      launch_arg_count() {
        return 0;
      },
      launch_arg_get() {
        return -2;
      },
      cap_has(id) {
        const known = [1, 2, 3, 10, 11, 12, 20, 21, 22, 23, 24, 25, 27, 28];
        return known.indexOf(id | 0) >= 0 ? 1 : 0;
      },
      rec_get_state() {
        return recState;
      },
      rec_is_starting() {
        return 0;
      },
      rec_start() {
        if (recState === 1) return -7; /* NS_ERR_BUSY */
        recState = 1;
        recStartMs = Date.now();
        recAccumMs = 0;
        return 0;
      },
      rec_stop() {
        if (recState === 0) return 0;
        if (recState === 1) {
          recAccumMs += Date.now() - recStartMs;
        }
        recState = 0;
        return 0;
      },
      rec_pause() {
        if (recState !== 1) return -1;
        recAccumMs += Date.now() - recStartMs;
        recState = 2;
        return 0;
      },
      rec_resume() {
        if (recState !== 2) return -1;
        recState = 1;
        recStartMs = Date.now();
        return 0;
      },
      rec_elapsed_ms() {
        if (recState === 1) {
          return (recAccumMs + (Date.now() - recStartMs)) | 0;
        }
        return recAccumMs | 0;
      },
      rec_filename(ptr, cap) {
        const mem = link.memory;
        if (!mem || ptr <= 0 || cap <= 0 || ptr + cap > mem.buffer.byteLength) return -1;
        const buf = new Uint8Array(mem.buffer);
        const name = REC_FAKE_NAME;
        let n = name.length;
        if (n >= cap) n = cap - 1;
        for (let i = 0; i < n; i++) buf[(ptr >>> 0) + i] = name.charCodeAt(i);
        buf[(ptr >>> 0) + n] = 0;
        return n;
      },
      rec_add_marker(_ptr) {
        return recState === 1 || recState === 2 ? 0 : -1;
      },
      screen_is_on() {
        return screenOn && screenBright > 0 ? 1 : 0;
      },
      screen_on() {
        if (screenBright <= 0) screenBright = screenBrightSaved || 200;
        screenOn = 1;
        return 0;
      },
      screen_off() {
        if (screenBright > 0) screenBrightSaved = screenBright;
        screenBright = 0;
        screenOn = 0;
        return 0;
      },
      screen_brightness_get() {
        return screenOn ? screenBright : 0;
      },
      screen_brightness_set(level) {
        level = level | 0;
        level = Math.max(0, Math.min(255, level));
        screenBright = level;
        if (level > 0) screenBrightSaved = level;
        screenOn = level > 0 ? 1 : 0;
        return 0;
      },
      ...hardware.imports,
    },
  };
}

async function runWasm(bytes, appId = "anonymous") {
  if (!kvNamespaces.has(appId)) kvNamespaces.set(appId, new Map());
  kvStore = kvNamespaces.get(appId);
  kvWrites = 0;
  if (running) {
    exitFlag = 1;
    busyDelay(80);
  }
  exitFlag = 0;
  keyQueue = [];
  eventQ = [];
  timers = [];
  recState = 0;
  recStartMs = 0;
  recAccumMs = 0;
  screenOn = 1;
  screenBright = 200;
  screenBrightSaved = 200;
  link.memory = null;
  running = true;
  canvas = null;
  ctx = null;
  ensureCanvas();
  ctx.fillStyle = "#000";
  ctx.fillRect(0, 0, W, H);

  self.postMessage({ type: "status", text: "加载 wasm…" });

  const wasmBytes = unwrapNsp1(bytes);
  const imports = makeImports();
  // Log mutating calls and returned data, without flooding the log on frame polling.
  for (const name of ['rec_start','rec_stop','rec_pause','rec_resume','rec_filename','rec_add_marker',
      'screen_on','screen_off','screen_brightness_set','kv_set','kv_get','kv_remove']) {
    const original = imports.ns[name];
    imports.ns[name] = (...values) => {
      const args = name === 'rec_add_marker' ? {source: readCString(values[0], 64)} :
        name.startsWith('kv_') ? {key: readCString(values[0], 16)} : {values};
      const result = original(...values);
      if (name === 'rec_filename' && result >= 0) args.filename = readCString(values[0], values[1]);
      self.postMessage({type: 'hardware', name, args, result, at: performance.now(),
        recorder: {state: recState, elapsed_ms: imports.ns.rec_elapsed_ms()},
        screen: {on: !!screenOn, brightness: screenBright}});
      return result;
    };
  }
  self.postMessage({type: 'hardware', name: 'session_begin', args: {appId}, result: 0,
    recorder: {state: recState, elapsed_ms: 0}, screen: {on: !!screenOn, brightness: screenBright}});
  let result;
  try {
    result = await WebAssembly.instantiate(wasmBytes, imports);
  } catch (e) {
    running = false;
    let text = String(e && e.message ? e.message : e);
    if (e && e.name === "LinkError") {
      text = "LinkError: " + text + "（缺 import 或签名不匹配）";
    }
    self.postMessage({ type: "error", text });
    self.postMessage({ type: "stopped" });
    return;
  }

  const exports = result.instance.exports;
  if (exports.memory) {
    link.memory = exports.memory;
  }

  const onInit = exports.on_init || exports._on_init;
  const onDestroy = exports.on_destroy || exports._on_destroy;
  const start = exports.start || exports._start;
  if (typeof start !== "function") {
    running = false;
    self.postMessage({ type: "error", text: "缺少导出 start()" });
    self.postMessage({ type: "stopped" });
    return;
  }

  self.postMessage({ type: "status", text: "运行中 — 按住 OK 蓄力 / Esc=BACK" });
  try {
    if (typeof onInit === "function") onInit();
    start();
    if (typeof onDestroy === "function") onDestroy();
    self.postMessage({
      type: "status",
      text: exitFlag ? "已退出" : "start() 返回",
    });
  } catch (e) {
    if (typeof onDestroy === "function") {
      try {
        onDestroy();
      } catch (_d) {
        /* ignore */
      }
    }
    self.postMessage({
      type: "error",
      text: String(e && e.message ? e.message : e),
    });
  }
  hardware.reset();
  running = false;
  self.postMessage({ type: "stopped" });
}

self.onmessage = (ev) => {
  const msg = ev.data || {};
  if (msg.type === "init") {
    if (msg.hardwareSab) hardware.init(msg.hardwareSab);
    if (msg.keySab) {
      keySab = new Int32Array(msg.keySab);
    }
    if (msg.sleepSab) {
      sleepSab = new Int32Array(msg.sleepSab);
    }
    self.postMessage({
      type: "keys_ok",
    });
    self.postMessage({
      type: "status",
      text: keySab
        ? "就绪（按键 SAB 已开）— 点列表运行"
        : "就绪但无按键 SAB — 请用 serve.bat",
    });
  } else if (msg.type === "run") {
    runWasm(msg.bytes, msg.appId);
  } else if (msg.type === "key") {
    const key = msg.code | 0;
    const phase = msg.phase | 0;
    const packed = ((phase || 1) << 8) | (key & 0xff);
    pushKey(packed);
  } else if (msg.type === "stop") {
    exitFlag = 1;
    if (sleepSab) {
      Atomics.add(sleepSab, 0, 1);
      Atomics.notify(sleepSab, 0, 1);
    }
  }
};
