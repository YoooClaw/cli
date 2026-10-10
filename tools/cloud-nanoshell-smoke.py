#!/usr/bin/env python3
"""Exercise a real CLI process and SDK over a loopback WebSocket Relay fixture.
Uses only stdlib; writes all test credentials, projects and evidence under --out.
"""
import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import socket
import struct
import subprocess
import sys
import time
import urllib.request
import io
import zipfile

ap = argparse.ArgumentParser(description=__doc__)
ap.add_argument('--cli', required=True)
ap.add_argument('--skill', required=True)
ap.add_argument('--out', required=True)
a = ap.parse_args()
root = Path(a.out).resolve()
root.mkdir(parents=True, exist_ok=False)
skill = Path(a.skill).resolve()
project = root / 'project'
home = root / 'cli-home'
profile = home / 'profiles/smoke'
profile.mkdir(parents=True)
env = dict(os.environ, YOOOCLAW_HOME=str(home), YOOOCLAW_PROFILE='smoke',
           PYTHONDONTWRITEBYTECODE='1', NS_CHROMIUM_EXECUTABLE='/opt/google/chrome/chrome')

def run(argv):
    result = subprocess.run(argv, env=env, capture_output=True, text=True, timeout=180)
    with (root / 'commands.log').open('a') as f:
        f.write(json.dumps(argv) + '\n' + result.stdout + result.stderr)
    if result.returncode:
        raise RuntimeError(result.stdout + result.stderr)
    return result.stdout

print('Building SDK counter and testing with system Chrome', flush=True)
pipe = [sys.executable, str(skill / 'scripts/pipeline.py')]
run(pipe + ['init', '--project', str(project)])
run(pipe + ['build', '--project', str(project), '--name', 'counter', '--id', 'com.yoooclaw.transportcheck'])
(project / 'acceptance.md').write_text('R1: starts at zero.\nR2: OK increments to one.\n')
(project / 'tests').mkdir()
(project / 'tests/counter.mjs').write_text('''export default async ({page, assert}) => {
 const read = () => page.evaluate(() => {
  const c=document.querySelector('#screen').getContext('2d'); let bits='';
  for(let y=0;y<5;y++)for(let x=0;x<3;x++)bits+=c.getImageData(41+x*3,33+y*3,1,1).data[0]>180?'1':'0';
  return bits;
 });
 assert.equal(await read(),'111101101101111');
 await page.keyboard.press('Enter');await page.waitForTimeout(120);
 assert.equal(await read(),'010010010010010');return ['R1','R2'];
};\n''')
run(['node', str(skill / 'scripts/test_app.mjs'), '--project', str(project), '--name', 'counter', '--scenario', 'tests/counter.mjs'])
# Approval is simulated only for this isolated test fixture, never a user's app.
run(pipe + ['release', '--project', str(project), '--name', 'counter', '--approved'])
cli = [a.cli, '--profile', 'smoke', '--format', 'json']
(home / 'credentials.json').write_text(json.dumps({'apiKeys': [{'label': 'smoke-phone', 'key': 'isolated-smoke-key', 'default': True}]}))
(profile / 'credentials.json').write_text(json.dumps({'gatewayToken': 'isolated-smoke-token'}))
for p in [home / 'credentials.json', profile / 'credentials.json']:
    p.chmod(0o600)
wasm = (project / 'dist/counter.nsp/app.wasm').read_bytes()
archive = (project / 'release/counter.zip').read_bytes()
package_id = hashlib.sha256(archive).hexdigest()
run(cli + ['nanoshell', 'publish', '--package', str(project / 'release/counter.zip'), '--client', 'smoke-phone'])
second = run(cli + ['nanoshell', 'publish', '--package', str(project / 'release/counter.zip'), '--client', 'smoke-phone'])
assert '"duplicated":true' in second.replace(' ', '')
assert package_id in run(cli + ['nanoshell', 'list', '--client', 'smoke-phone'])
assert any((profile / 'nanoshell').glob('zip-*/package.zip'))
print('Published real program, duplicate publication and list passed', flush=True)
relay = socket.socket()
relay.bind(('127.0.0.1', 0))
relay.listen()
relay.settimeout(30)
probe = socket.socket()
probe.bind(('127.0.0.1', 0))
port = probe.getsockname()[1]
probe.close()
(profile / 'config.json').write_text(json.dumps({
 'relay': {'url': 'ws://127.0.0.1:%d/relay' % relay.getsockname()[1], 'enabled': True},
 'daemon': {'bind': '127.0.0.1', 'port': port}, 'autoUpdate': {'enabled': False}}))
log = (root / 'daemon.log').open('w')
process = subprocess.Popen(cli + ['daemon', 'run-foreground', '--bind', '127.0.0.1', '--port', str(port)], env=env, stdout=log, stderr=log)
conn = None
try:
    conn, _ = relay.accept()
    conn.settimeout(15)
    headers = b''
    while b'\r\n\r\n' not in headers:
        part = conn.recv(4096)
        if not part or len(headers) > 16384:
            raise RuntimeError('bad WebSocket handshake')
        headers += part
    key = next(line.split(':', 1)[1].strip() for line in headers.decode().split('\r\n') if line.lower().startswith('sec-websocket-key:'))
    accept = base64.b64encode(hashlib.sha1((key + '258EAFA5-E914-47DA-95CA-C5AB0DC85B11').encode()).digest()).decode()
    conn.sendall(('HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: ' + accept + '\r\n\r\n').encode())

    def send(data, opcode=1):
        if isinstance(data, str): data = data.encode()
        n = len(data)
        h = bytes([0x80 | opcode]) + (bytes([n]) if n < 126 else b'\x7e' + struct.pack('!H', n))
        conn.sendall(h + data)

    def exact(n):
        data = b''
        while len(data) < n:
            chunk = conn.recv(n - len(data))
            if not chunk: raise RuntimeError('WebSocket closed')
            data += chunk
        return data

    def receive():
        message = b''
        while True:
            h = exact(2); n = h[1] & 127
            if n == 126: n = struct.unpack('!H', exact(2))[0]
            elif n == 127: n = struct.unpack('!Q', exact(8))[0]
            if n > 256 * 1024: raise RuntimeError('oversized frame')
            mask = exact(4) if h[1] & 128 else b''
            data = exact(n)
            if mask: data = bytes(v ^ mask[i % 4] for i, v in enumerate(data))
            op = h[0] & 15
            if op == 9: send(data, 10); continue
            if op == 10: continue
            if op == 8: raise RuntimeError('WebSocket close frame')
            message += data
            if h[0] & 128:
                if message == b'ping': send('pong'); message = b''; continue
                if message == b'pong': message = b''; continue
                return json.loads(message)

    def rpc(method, params):
        ident = 'smoke-' + str(time.time_ns())
        send(json.dumps({'type': 'req', 'id': ident, 'method': method, 'params': params}))
        while True:
            res = receive()
            if res.get('id') == ident:
                assert res['type'] == 'res'
                return res

    listing = rpc('nanoshell.apps.list', {})
    assert listing['ok'] and len(listing['payload']['items']) == 1, listing
    item = listing['payload']['items'][0]
    assert item['packageId'] == package_id and item['packageBytes'] == len(archive)
    download = rpc('nanoshell.apps.download', {'packageId': item['packageId']})
    assert download['ok'], download
    data = download['payload']
    decoded = base64.b64decode(data['data'], validate=True)
    assert decoded == archive and data['size'] == len(archive)
    assert data['fileName'] == 'counter.zip' and data['contentType'] == 'application/zip'
    with zipfile.ZipFile(io.BytesIO(decoded)) as z:
        assert z.read('counter.nsp/app.wasm') == wasm
        assert json.loads(z.read('counter.nsp/manifest.json'))['id'] == item['appId']
    assert wasm[:4] == b'NSP1' and struct.unpack('<I', wasm[8:12])[0] == len(wasm) - 16
    assert hashlib.sha256(decoded).hexdigest() == data['packageId']
    (root / 'downloaded-counter.zip').write_bytes(decoded)
    (root / 'websocket-list.json').write_text(json.dumps(listing, indent=2))
    (root / 'websocket-download.json').write_text(json.dumps(download, indent=2))
    missing = rpc('nanoshell.apps.download', {'packageId': '0' * 64})
    assert not missing['ok'] and missing['error']['code'] == 'PACKAGE_NOT_FOUND'
    invalid = rpc('nanoshell.apps.list', {'limit': 0})
    assert not invalid['ok'] and invalid['error']['code'] == 'INVALID_PARAMS'
    req = urllib.request.Request('http://127.0.0.1:%d/gateway/nanoshell.apps.list' % port, data=b'{}', headers={
      'Authorization': 'Bearer isolated-smoke-token', 'x-openclaw-relay-internal': '1', 'x-yoooclaw-internal-client-label': 'other-phone'})
    with urllib.request.urlopen(req, timeout=10) as r: other = json.load(r)
    assert other['ok'] and other['data']['items'] == []
    report = {'status': 'passed', 'cliVersion': run([a.cli, '--version']).strip(), 'programBytes': len(wasm), 'packageBytes': len(archive),
      'packageId': package_id, 'format': 'ZIP containing NSP1 program', 'contentType': data['contentType'],
      'checks': ['Chrome gameplay', 'SDK build/release', 'CLI publish/list', 'duplicate publication',
                 'real CLI WebSocket list/download', 'byte equality and SHA-256', 'NSP1 preserved', 'error envelopes', 'client isolation'],
      'scope': 'Cloud machine, actual CLI and Chrome, loopback Relay fixture; production Relay/App/hardware not tested'}
    (root / 'report.json').write_text(json.dumps(report, indent=2))
    print(json.dumps(report), flush=True)
finally:
    process.terminate()
    try: process.wait(timeout=15)
    except subprocess.TimeoutExpired: process.kill(); process.wait()
    if conn: conn.close()
    relay.close()
    log.close()
