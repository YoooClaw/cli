"""Local-only tests: result saving, ready-to-paste reply and the Stop hook link guard."""
import http.server
import io
import json
import subprocess
import sys
import tempfile
import threading
import unittest
from pathlib import Path
SCRIPTS = Path(__file__).resolve().parents[1] / 'skills/yoooclaw-media-generate/scripts'
sys.path.insert(0, str(SCRIPTS))
import client

GENUINE = ('https://dashscope-result.oss-cn-beijing.aliyuncs.com/1d/7f3c9e1a4b2d4c8e9a0b1c2d3e4f5a6b.png'
           '?Expires=1790300000&OSSAccessKeyId=LTAI5tAbCdEf&Signature=Zk3%2BqWmN8vT%3D')
OTHER = GENUINE.replace('7f3c9e1a4b2d4c8e9a0b1c2d3e4f5a6b', '0a9b8c7d6e5f4a3b2c1d0e9f8a7b6c5d')
TYPO = GENUINE.replace('Zk3%2B', 'Zk3%2')
PNG = b'\x89PNG\r\n\x1a\n' + b'\0' * 32


def run_hook(transcript_entries, last_message, stop_hook_active=False):
    with tempfile.TemporaryDirectory() as folder:
        transcript = Path(folder) / 'transcript.jsonl'
        transcript.write_text('\n'.join(json.dumps(e, ensure_ascii=False) for e in transcript_entries), encoding='utf-8')
        payload = {'hook_event_name': 'Stop', 'transcript_path': str(transcript),
                   'last_assistant_message': last_message, 'stop_hook_active': stop_hook_active}
        proc = subprocess.run([sys.executable, str(SCRIPTS / 'link_guard.py')], input=json.dumps(payload),
                              capture_output=True, text=True, timeout=30)
    return proc.returncode, proc.stdout, proc.stderr


def tool_result(output):
    return {'type': 'user', 'message': {'role': 'user', 'content': [
        {'type': 'tool_result', 'tool_use_id': 't1', 'content': output}]},
        'toolUseResult': {'stdout': output, 'stderr': ''}}


SCRIPT_OUTPUT = json.dumps({'status': 'success', 'images': [GENUINE, OTHER]})


class LinkGuardHookTests(unittest.TestCase):
    def test_blocks_mistyped_link_and_hands_back_exact_url(self):
        code, _, err = run_hook([tool_result(SCRIPT_OUTPUT)], f'好了：[小猫图片（点击下载）]({TYPO})')
        self.assertEqual(code, 2)
        self.assertIn(GENUINE, err)
        self.assertIn(TYPO, err)

    def test_catches_truncated_and_escaped_links(self):
        for written in (GENUINE[:70] + '…', GENUINE.replace('.png', '\\_x.png').replace('\\_x', '\\_', 1)):
            with self.subTest(written=written):
                code, _, err = run_hook([tool_result(SCRIPT_OUTPUT)], f'[下载]({written})')
                self.assertEqual(code, 2)
                self.assertIn(GENUINE, err)

    def test_passes_exact_links_and_unrelated_links(self):
        text = f'[1]({GENUINE})\n[2]({OTHER})\n文档 https://help.yoooclaw.com/media 。'
        self.assertEqual(run_hook([tool_result(SCRIPT_OUTPUT)], text)[0], 0)

    def test_only_trusts_tool_results(self):
        assistant_echo = {'type': 'assistant', 'message': {'role': 'assistant', 'content': [
            {'type': 'text', 'text': json.dumps({'images': [TYPO]})}]}}
        self.assertEqual(run_hook([assistant_echo], f'[下载]({GENUINE})')[0], 0)

    def test_second_attempt_shows_correct_link_instead_of_looping(self):
        code, out, _ = run_hook([tool_result(SCRIPT_OUTPUT)], f'[下载]({TYPO})', stop_hook_active=True)
        self.assertEqual(code, 0)
        self.assertIn(GENUINE, json.loads(out)['systemMessage'])

    def test_bad_input_never_blocks(self):
        proc = subprocess.run([sys.executable, str(SCRIPTS / 'link_guard.py')], input='not json',
                              capture_output=True, text=True, timeout=30)
        self.assertEqual(proc.returncode, 0)


class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        body, mime = {'/a.png': (PNG, 'image/png'), '/err': (b'{}', 'application/json')}.get(self.path.split('?')[0], (b'', 'image/png'))
        self.send_response(200)
        self.send_header('Content-Type', mime)
        self.send_header('Content-Length', str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *args):
        pass


class SaveResultsTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Handler)
        threading.Thread(target=cls.server.serve_forever, daemon=True).start()
        cls.base = f'http://127.0.0.1:{cls.server.server_port}'

    @classmethod
    def tearDownClass(cls):
        cls.server.shutdown()

    def test_downloads_and_builds_reply_with_untouched_urls(self):
        urls = [self.base + '/a.png?Signature=a%2Bb', self.base + '/err', self.base + '/empty.png']
        with tempfile.TemporaryDirectory() as folder:
            result = client.save_results('image', urls, '日落[海边](小猫)', folder)
            self.assertEqual(len(result['files']), 1)
            self.assertEqual(Path(result['files'][0]).read_bytes(), PNG)
            self.assertEqual(result['download_failed'], 2)
            self.assertEqual([p.name for p in Path(folder).iterdir() if p.name.startswith('.download-')], [])
        lines = result['reply_markdown'].splitlines()
        self.assertEqual(len(lines), 3)
        self.assertEqual(lines[0], f'[日落 海边 小猫图片 1（点击下载）]({urls[0]})')
        # hook 取到的真实链接与交付文本逐字一致
        self.assertEqual(run_hook([tool_result(json.dumps({'images': urls}))], result['reply_markdown'])[0], 0)


if __name__ == '__main__':
    unittest.main()
