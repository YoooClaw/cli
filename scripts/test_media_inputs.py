"""Run with python3 -B scripts/test_media_inputs.py; no network or credentials needed."""
import base64
import tempfile
import contextlib
import importlib.util
import io
import sys
import unittest
from pathlib import Path
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
SCRIPTS = ROOT / 'skills/yoooclaw-media-generate/scripts'
sys.path.insert(0, str(SCRIPTS))
import video_generate as video


class VideoInputsTests(unittest.TestCase):
    def invoke(self, extra):
        args = ['video_generate.py', 'generate', '--resolution', '720P', '--seconds', '30',
                '--prompt', '缓慢转头', '--confirmed'] + extra
        with patch.object(sys, 'argv', args), contextlib.redirect_stdout(io.StringIO()):
            return video.main()

    def test_input_modes_preserve_urls(self):
        start = 'https://example.com/start.png?Signature=a%2Bb%3D&Expires=123'
        end = 'https://example.com/end.png'
        for extra, expected in [
            ([], []),
            (['--image-url', start], [{'type': 'reference_image', 'url': start}]),
            (['--first-frame-url', start], [{'type': 'first_frame', 'url': start}]),
            (['--first-frame-url', start, '--last-frame-url', end],
             [{'type': 'first_frame', 'url': start}, {'type': 'last_frame', 'url': end}]),
        ]:
            with self.subTest(extra=extra), patch.object(video.client, 'request', return_value={
                'output': {'task_id': 'task-1', 'task_status': 'PENDING'}
            }) as request:
                self.assertEqual(self.invoke(extra), 0)
                request.assert_called_once()
                method, route, body = request.call_args.args
                self.assertEqual((method, route), ('POST', '/videos/generations'))
                self.assertEqual(body['input'], {'prompt': '缓慢转头', **({'media': expected} if expected else {})})
                self.assertEqual(body['parameters'], {'duration': 30, 'resolution': '720P'})

    def test_local_and_base64_inputs(self):
        data = base64.b64decode('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+a0n8AAAAASUVORK5CYII=')
        encoded = 'data:image/png;base64,' + base64.b64encode(data).decode()
        with tempfile.TemporaryDirectory() as folder:
            path = Path(folder) / 'input.png'
            path.write_bytes(data)
            for flag, kind in [('--image', 'reference_image'), ('--image-url', 'reference_image'),
                               ('--first-frame', 'first_frame'), ('--first-frame-url', 'first_frame'),
                               ('--last-frame', 'last_frame'), ('--last-frame-url', 'last_frame')]:
                for value in (str(path), encoded):
                    extra = [flag, value]
                    expected = [{'type': kind, 'url': encoded}]
                    if kind == 'last_frame':
                        extra += ['--first-frame', str(path)]
                        expected.insert(0, {'type': 'first_frame', 'url': encoded})
                    with self.subTest(flag=flag, local=value == str(path)), patch.object(video.client, 'request', return_value={
                        'output': {'task_id': 'task-1', 'task_status': 'PENDING'}
                    }) as request:
                        self.assertEqual(self.invoke(extra), 0)
                        request.assert_called_once()
                        self.assertEqual(request.call_args.args[2]['input']['media'], expected)

    def test_invalid_inputs_do_not_submit(self):
        url = 'https://example.com/a.jpg'
        for extra in [
            ['--last-frame-url', url],
            ['--image-url', url, '--first-frame-url', url],
            ['--image-url', url, '--last-frame-url', url],
            ['--image-url', '/does-not-exist/input.png'],
            ['--image', 'data:image/jpeg;base64,iVBORw0KGgo='],
            ['--image', 'ftp://example.com/a.png'],
            ['--image-url', 'data:image/png;base64,abc'],
            ['--image-url', 'https://user:pass@example.com/a.png'],
            ['--image-url', 'https://example.com/a b.png'],
            ['--image-url', 'https://example.com:bad/a.png'],
            ['--image-url', ''],
        ]:
            with self.subTest(extra=extra), patch.object(video.client, 'request') as request:
                self.assertEqual(self.invoke(extra), 1)
                request.assert_not_called()

    def test_duration_limits_before_network(self):
        for command in ('estimate', 'generate'):
            for seconds in ('1', '0', '-1', '1.5', '2.0', 'abc', '2s', '31'):
                args = ['video_generate.py', command, '--resolution', '480P', '--seconds', seconds]
                if command == 'generate':
                    args += ['--prompt', '猫', '--confirmed']
                with patch.object(sys, 'argv', args), patch.object(video.client, 'request') as request, contextlib.redirect_stderr(io.StringIO()) as errors:
                    with self.assertRaises(SystemExit) as caught:
                        video.main()
                    self.assertEqual(caught.exception.code, 2)
                    request.assert_not_called()
                    if seconds in ('1.5', '2.0', 'abc', '2s'):
                        self.assertIn('视频时长必须使用整数秒', errors.getvalue())
            for seconds in ('2', '30'):
                args = ['video_generate.py', command, '--resolution', '480P', '--seconds', seconds]
                response = {'credits': 100}
                if command == 'generate':
                    args += ['--prompt', '猫', '--confirmed']
                    response = {'output': {'task_id': 'x', 'task_status': 'PENDING'}}
                with patch.object(sys, 'argv', args), patch.object(video.client, 'request', return_value=response) as request, contextlib.redirect_stdout(io.StringIO()):
                    self.assertEqual(video.main(), 0)
                if command == 'generate':
                    self.assertEqual(request.call_args.args[2]['parameters']['duration'], int(seconds))
                else:
                    self.assertIn('seconds=' + seconds, request.call_args.args[1])

    def test_authorization_still_required(self):
        with patch.object(video.client, 'request') as request:
            self.assertEqual(self.invoke(['--image-url', 'https://example.com/a.png', '--prompt', '']), 1)
            request.assert_not_called()
        args = ['video_generate.py', 'generate', '--resolution', '720P', '--seconds', '5',
                '--prompt', '转头', '--image-url', 'https://example.com/a.png']
        with patch.object(sys, 'argv', args), patch.object(video.client, 'request') as request, contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(video.main(), 1)
            request.assert_not_called()


if __name__ == '__main__':
    unittest.main()
