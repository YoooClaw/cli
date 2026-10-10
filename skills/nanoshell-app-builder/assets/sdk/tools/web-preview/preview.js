import {createHardwarePanel} from "./hardware.js";
const hardwarePanel = createHardwarePanel();
const screen = document.getElementById("screen");
const ctx = screen.getContext("2d");
ctx.imageSmoothingEnabled = false;

const drop = document.getElementById("drop");
const fileInput = document.getElementById("file");
const statusEl = document.getElementById("status");
const btnStop = document.getElementById("btnStop");
const demoList = document.getElementById("demoList");
const isoBanner = document.getElementById("isoBanner");
const keyFlash = document.getElementById("keyFlash");

const NS_KEY = { NONE: 0, LEFT: 1, RIGHT: 2, UP: 3, DOWN: 4, OK: 5, BACK: 6 };

const FALLBACK_DEMOS = [
  { id: "com.example.hop", name: "Hop", file: "hop.nsp/app.wasm", web: true },
  { id: "com.example.snake", name: "Snake", file: "snake.nsp/app.wasm", web: true },
  { id: "com.example.counter", name: "Counter", file: "counter.nsp/app.wasm", web: true },
  { id: "com.example.hello_tiny", name: "Hello Tiny", file: "hello_tiny.nsp/app.wasm", web: true },
  { id: "com.example.wasm_hello", name: "Hello", file: "wasm_hello.nsp/app.wasm", web: true },
];

let worker = null;
let demos = FALLBACK_DEMOS;
/** @type {SharedArrayBuffer | null} */
let keySabBuf = null;
/** @type {Int32Array | null} */
let keySabView = null;
/** sleep pad for Atomics.wait in worker */
/** @type {SharedArrayBuffer | null} */
let sleepSabBuf = null;
let okRepeatTimer = 0;
let keysReady = false;

function setStatus(t) {
  statusEl.textContent = t;
}

function flashKey(label) {
  if (!keyFlash) return;
  keyFlash.textContent = label;
  keyFlash.classList.add("on");
  clearTimeout(flashKey._t);
  flashKey._t = setTimeout(() => keyFlash.classList.remove("on"), 120);
}

function isolationOk() {
  return typeof crossOriginIsolated !== "undefined" && crossOriginIsolated;
}

function updateIsoBanner() {
  if (!isoBanner) return;
  if (isolationOk() && typeof SharedArrayBuffer !== "undefined") {
    isoBanner.hidden = true;
    return;
  }
  isoBanner.hidden = false;
  isoBanner.innerHTML =
    "<strong>按键不可用</strong>：当前页面没有跨源隔离（SharedArrayBuffer）。" +
    "请关掉旧终端里的 http.server，运行 <code>tools\\web-preview\\serve.bat</code>，" +
    "再打开 <code>服务启动日志中的地址</code> 并强刷（Ctrl+F5）。" +
    " <span class='mono'>isolated=" +
    String(!!isolationOk()) +
    " SAB=" +
    typeof SharedArrayBuffer +
    "</span>";
}

function ensureSab() {
  if (keySabBuf && sleepSabBuf) return true;
  if (!isolationOk() || typeof SharedArrayBuffer === "undefined") {
    return false;
  }
  try {
    keySabBuf = new SharedArrayBuffer(20 * 4);
    keySabView = new Int32Array(keySabBuf);
    Atomics.store(keySabView, 0, 0); /* write */
    Atomics.store(keySabView, 1, 0); /* read */
    sleepSabBuf = new SharedArrayBuffer(4);
    Atomics.store(new Int32Array(sleepSabBuf), 0, 0);
    return true;
  } catch (e) {
    console.error(e);
    return false;
  }
}

function ensureWorker() {
  if (worker) return worker;
  worker = new Worker(new URL("./ns-worker.js", import.meta.url));
  worker.onmessage = (ev) => {
    const msg = ev.data || {};
    hardwarePanel.accept(msg);
    if (msg.type === "frame" && msg.bitmap) {
      ctx.clearRect(0, 0, screen.width, screen.height);
      ctx.drawImage(msg.bitmap, 0, 0);
      msg.bitmap.close();
    } else if (msg.type === "status") {
      setStatus(msg.text);
    } else if (msg.type === "error") {
      setStatus("错误: " + msg.text);
      console.error(msg.text);
    } else if (msg.type === "stopped") {
      btnStop.disabled = true;
      stopOkRepeat();
    } else if (msg.type === "keys_ok") {
      keysReady = true;
      setStatus("按键通道 OK — 点列表运行（按住 OK 蓄力）");
    }
  };
  worker.onerror = (e) => {
    setStatus("Worker 错误: " + (e.message || e));
  };

  if (ensureSab()) {
    try {
      worker.postMessage(
        { type: "init", keySab: keySabBuf, sleepSab: sleepSabBuf, hardwareSab: hardwarePanel.sab },
        [/* SAB is not transfer-list; shared by reference */]
      );
      keysReady = true;
    } catch (e) {
      keysReady = false;
      console.error("postMessage SAB failed", e);
      setStatus("无法把按键缓冲交给 Worker: " + e);
    }
  } else {
    keysReady = false;
    updateIsoBanner();
  }
  return worker;
}

function sendKey(code, phase) {
  const ph = phase || 1;
  const name =
    code === NS_KEY.OK
      ? ph === 3
        ? "OK~"
        : ph === 2
          ? "OK↑"
          : "OK↓"
      : code === NS_KEY.BACK
        ? "BACK"
        : "K" + code;
  flashKey(name);

  if (!keySabView || !keysReady) {
    /* last-resort: may not arrive while wasm busy-waits */
    ensureWorker().postMessage({ type: "key", code, phase: ph });
    return;
  }
  const packed = ((ph & 0xff) << 8) | (code & 0xff);
  const w = Atomics.load(keySabView, 0);
  const r = Atomics.load(keySabView, 1);
  if (((w - r) | 0) >= 16) {
    Atomics.store(keySabView, 1, (r + 1) | 0);
  }
  Atomics.store(keySabView, 2 + (w & 15), packed);
  Atomics.store(keySabView, 0, (w + 1) | 0);
  /* wake worker if it is in Atomics.wait */
  if (sleepSabBuf) {
    const sv = new Int32Array(sleepSabBuf);
    Atomics.add(sv, 0, 1);
    Atomics.notify(sv, 0, 1);
  }
}

function stopOkRepeat() {
  if (okRepeatTimer) {
    clearInterval(okRepeatTimer);
    okRepeatTimer = 0;
  }
}

function startOkRepeat() {
  stopOkRepeat();
  okRepeatTimer = window.setInterval(() => sendKey(NS_KEY.OK, 3), 40);
}

async function runBytes(buf, label, appId) {
  updateIsoBanner();
  if (!keysReady) {
    setStatus("请先用 serve.bat 打开本页（按键通道未就绪）");
    return;
  }
  const w = ensureWorker();
  btnStop.disabled = false;
  setStatus("启动 " + (label || "app.wasm") + "…");
  w.postMessage({ type: "run", bytes: buf, appId: appId || "file:" + label }, [buf]);
}

async function runFile(file) {
  await runBytes(await file.arrayBuffer(), file.name);
}

async function runSample(file, label, appId) {
  try {
    const res = await fetch("/dist/" + file + "?t=" + Date.now());
    if (!res.ok) throw new Error("HTTP " + res.status);
    await runBytes(await res.arrayBuffer(), label || file, appId || file);
  } catch (e) {
    setStatus("样例加载失败: " + e);
  }
}

function wasmFileFor(demo) {
  if (demo.file) return demo.file;
  if (demo.nsp) return String(demo.nsp).replace(/\/?$/, "") + "/app.wasm";
  if (!demo.web) return null;
  const src = (demo.src || "").replace(/\\/g, "/");
  const m = /guest\/wasm\/([^.\\/]+)\.c$/.exec(src);
  if (m) return m[1] + ".nsp/app.wasm";
  const id = (demo.id || "").split(".").pop();
  return id ? id + ".nsp/app.wasm" : null;
}

function renderDemoList() {
  if (!demoList) return;
  demoList.innerHTML = "";
  const list = demos.filter((d) => d.web && wasmFileFor(d));
  list.forEach((d) => {
    const row = document.createElement("div");
    row.className = "demo-row";
    const title = document.createElement("div");
    title.className = "demo-title";
    title.textContent = d.name || d.id;
    const tag = document.createElement("span");
    tag.className = "demo-tag";
    tag.textContent = "wasm";
    title.appendChild(tag);
    row.appendChild(title);
    const btn = document.createElement("button");
    btn.type = "button";
    btn.textContent = "运行";
    btn.disabled = !keysReady;
    btn.title = keysReady ? "" : "需要 serve.bat（跨源隔离）";
    btn.addEventListener("click", () => runSample(wasmFileFor(d), d.name, d.id));
    row.appendChild(btn);
    demoList.appendChild(row);
  });
}

async function loadCatalog() {
  try {
    const res = await fetch("/dist/catalog.json?t=" + Date.now());
    if (!res.ok) throw new Error("no catalog");
    const data = await res.json();
    if (Array.isArray(data.demos) && data.demos.length) demos = data.demos;
  } catch (_e) {
    /* fallback */
  }
  renderDemoList();
}

drop.addEventListener("click", () => fileInput.click());
fileInput.addEventListener("change", () => {
  const f = fileInput.files && fileInput.files[0];
  if (f) runFile(f);
});
["dragenter", "dragover"].forEach((ev) => {
  drop.addEventListener(ev, (e) => {
    e.preventDefault();
    drop.classList.add("drag");
  });
});
["dragleave", "drop"].forEach((ev) => {
  drop.addEventListener(ev, (e) => {
    e.preventDefault();
    drop.classList.remove("drag");
  });
});
drop.addEventListener("drop", (e) => {
  const f = e.dataTransfer.files && e.dataTransfer.files[0];
  if (f) runFile(f);
});

btnStop.addEventListener("click", () => {
  if (worker) worker.postMessage({ type: "stop" });
  stopOkRepeat();
  setStatus("正在停止…");
});

document.querySelectorAll(".keys [data-key]").forEach((btn) => {
  const code = Number(btn.dataset.key);
  btn.addEventListener("pointerdown", (e) => {
    e.preventDefault();
    try {
      btn.setPointerCapture(e.pointerId);
    } catch (_e) {
      /* ignore */
    }
    sendKey(code, 1);
    if (code === NS_KEY.OK) startOkRepeat();
  });
  const up = (e) => {
    e.preventDefault();
    if (code === NS_KEY.OK) stopOkRepeat();
    sendKey(code, 2);
  };
  btn.addEventListener("pointerup", up);
  btn.addEventListener("pointercancel", up);
});

window.addEventListener("keydown", (e) => {
  if (e.target.closest?.("input,textarea,select,[contenteditable=true]")) return;
  let code = 0;
  if (e.key === "Enter" || e.key === " ") code = NS_KEY.OK;
  else if (e.key === "Escape" || e.key === "Backspace") code = NS_KEY.BACK;
  if (!code) return;
  e.preventDefault();
  if (e.repeat) {
    sendKey(code, 3);
    return;
  }
  sendKey(code, 1);
  if (code === NS_KEY.OK) startOkRepeat();
});
window.addEventListener("keyup", (e) => {
  if (e.target.closest?.("input,textarea,select,[contenteditable=true]")) return;
  let code = 0;
  if (e.key === "Enter" || e.key === " ") code = NS_KEY.OK;
  else if (e.key === "Escape" || e.key === "Backspace") code = NS_KEY.BACK;
  if (!code) return;
  e.preventDefault();
  if (code === NS_KEY.OK) stopOkRepeat();
  sendKey(code, 2);
});

updateIsoBanner();
ensureWorker();
loadCatalog();
setStatus(
  keysReady
    ? "就绪 — 按住 OK / Esc=BACK"
    : "按键未就绪 — 请用 serve.bat 打开本页"
);
