/* Browser-only peripherals; no real Bluetooth or vibration permissions required. */
export function createHardwarePanel() {
  const panel = document.querySelector('#hardware');
  const sab = typeof SharedArrayBuffer === 'function' ? new SharedArrayBuffer((16 + 8 * 201) * 4) : null;
  const shared = sab ? new Int32Array(sab) : null;
  if (shared) { shared[0] = 87; shared[2] = 1; }
  const calls = [];
  let dropped = 0, sequence = 0, led = null, motor = null;
  let recorder = {state: 0, elapsed_ms: 0}, display = {on: true, brightness: 200};
  let recorderUpdated = performance.now();
  const $ = id => document.getElementById(id);
  function record(call) {
    calls.push({...call, sequence: ++sequence});
    if (calls.length > 2000) { calls.shift(); dropped++; }
    if (call.name === 'ble_send') $('bleOutput').textContent = call.result < 0 ? `发送失败 (${call.result})` : call.args.hex;
    $('hardwareLog').textContent = calls.slice(-6).map(c => `${c.sequence} · ${c.name} ${JSON.stringify(c.args)} → ${c.result}`).join('\n');
  }
  function enqueue(kind, data) {
    if (!shared) throw new Error('请使用支持跨源隔离的预览服务');
    const max = kind === 'ble' ? 180 : 200;
    if (!data.length || data.length > max) throw new Error(`数据长度需为 1–${max} 字节`);
    if (kind === 'ble' && !Atomics.load(shared, 2)) throw new Error('模拟 BLE 未连接');
    const h = kind === 'ble' ? 3 : 6, base = kind === 'ble' ? 16 : 820;
    if (Atomics.compareExchange(shared, h, 0, 1) !== 0) throw new Error('接收队列忙，请重试');
    try {
      const w = Atomics.load(shared, h + 1), r = Atomics.load(shared, h + 2);
      if (w - r >= 4) {
        if (kind === 'ble') throw new Error('BLE 队列已满（4 包）');
        Atomics.store(shared, h + 2, r + 1);
      }
      const offset = base + (w % 4) * 201;
      shared[offset] = data.length;
      data.forEach((b, i) => { shared[offset + 1 + i] = b; });
      Atomics.store(shared, h + 1, w + 1);
    } finally { Atomics.store(shared, h, 0); }
  }
  const api = {
    calls,
    get dropped() { return dropped; },
    get sequence() { return sequence; },
    setBattery(percent, charge = 0) {
      if (!Number.isInteger(percent) || percent < 0 || percent > 100 || ![0, 1, 2].includes(charge)) throw new Error('无效电池状态');
      if (!shared) throw new Error('共享内存不可用');
      Atomics.store(shared, 0, percent); Atomics.store(shared, 1, charge);
      $('battery').value = percent; $('batteryValue').textContent = `${percent}%`; $('charge').value = charge;
    },
    setBleConnected(value) {
      if (!shared) throw new Error('共享内存不可用');
      Atomics.store(shared, 2, value ? 1 : 0); $('bleConnected').checked = !!value;
    },
    injectBle(hex) {
      const s = hex.replace(/\s/g, '');
      if (!/^(?:[0-9a-fA-F]{2})+$/.test(s)) throw new Error('请输入成对的十六进制字节，如 01 ff');
      enqueue('ble', Uint8Array.from(s.match(/../g), b => parseInt(b, 16)));
    },
    injectNotification(title, body) {
      const encoder = new TextEncoder(), t = encoder.encode(title), b = encoder.encode(body);
      if (t.length + b.length + 2 > 200) throw new Error('标题和正文合计最多 198 个 UTF-8 字节');
      enqueue('notify', new Uint8Array([t.length, ...t, b.length, ...b]));
    },
    snapshot() { return {calls: calls.map(c => ({...c})), dropped, led, motor, recorder, screen: display}; },
  };
  window.nsHardware = api;
  const action = fn => { try { fn(); $('hardwareFeedback').textContent = '已更新模拟设备'; } catch (e) { $('hardwareFeedback').textContent = e.message; } };
  $('battery').oninput = () => action(() => api.setBattery(Number($('battery').value), Number($('charge').value)));
  $('charge').onchange = $('battery').oninput;
  $('bleConnected').onchange = () => action(() => api.setBleConnected($('bleConnected').checked));
  $('sendBle').onclick = () => action(() => api.injectBle($('bleInput').value));
  $('sendNotify').onclick = () => action(() => api.injectNotification($('notifyTitle').value, $('notifyBody').value));
  if (!shared) { panel.querySelectorAll('input,select,button').forEach(el => { el.disabled = true; }); }
  function accept(msg) {
    if (msg.type !== 'hardware') return;
    record(msg);
    if (msg.result < 0) return;
    const now = performance.now(), a = msg.args;
    if (msg.recorder) { recorder = msg.recorder; recorderUpdated = now; }
    if (msg.screen) { display = msg.screen; $('screen').style.filter = `brightness(${display.on ? display.brightness / 255 : 0})`; }
    if (msg.name === 'session_begin') { led = null; motor = null; }
    if (msg.name === 'led_effect') led = {...a, since: now};
    if (msg.name === 'led_off') led = null;
    if (msg.name === 'vibe_pulse') motor = {...a, repeat: !a.off_ms ? 1 : a.repeat, since: now};
    if (msg.name === 'vibe') motor = msg.args.pattern === 1 ? {on_ms: 30, off_ms: 0, repeat: 1, since: now} : {on_ms: 50, off_ms: 100, repeat: 2, since: now};
    if (msg.name === 'vibe_stop') motor = null;
    if (msg.name === 'host_reset') { led = null; motor = null; }
  }
  function render(now) {
    const ms = recorder.elapsed_ms + (recorder.state === 1 ? now - recorderUpdated : 0);
    $('recorderStatus').textContent = `${['未录音','录音中','已暂停'][recorder.state]} · ${(ms / 1000).toFixed(1)} 秒`;
    $('displayStatus').textContent = `${display.on && display.brightness ? '亮屏' : '熄屏'} · 亮度 ${display.brightness}/255`;
    const elapsed = led ? now - led.since : 0;
    const level = !led || !led.mode ? 0 : led.mode === 2 ? (elapsed % (led.on_ms + led.off_ms) < led.on_ms ? 1 : 0) : led.mode === 3 ? (1 - Math.cos(elapsed / 2400 * Math.PI * 2)) / 2 : 1;
    panel.querySelectorAll('.sim-led').forEach((el, i) => {
      const active = led && (led.mask & (1 << i));
      el.style.backgroundColor = active ? '#' + led.color.toString(16).padStart(6, '0') : 'var(--border)';
      el.style.opacity = String(active ? 0.12 + level * led.brightness / 255 * 0.88 : 0.12);
    });
    $('ledStatus').textContent = !led || !led.mode ? '灯光关闭' : `${['关闭','常亮','闪烁','呼吸'][led.mode]} · #${led.color.toString(16).padStart(6,'0')} · 亮度 ${led.brightness}`;
    if (motor && motor.repeat && now - motor.since >= (motor.repeat - 1) * (motor.on_ms + motor.off_ms) + motor.on_ms) motor = null;
    const vibrating = motor && (now - motor.since) % (motor.on_ms + motor.off_ms) < motor.on_ms;
    $('motorIndicator').classList.toggle('active', !!vibrating);
    $('motorStatus').textContent = motor ? `${motor.on_ms} ms 震动 / ${motor.off_ms} ms 间隔 · ${motor.repeat || '无限'} 次` : '马达停止';
    requestAnimationFrame(render);
  }
  requestAnimationFrame(render);
  return {sab, accept};
}
