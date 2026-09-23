"""Mock-only video polling regression tests."""
import contextlib
import io
import json
import sys
import unittest
from pathlib import Path
from unittest.mock import patch, MagicMock
sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'skills/yoooclaw-media-generate/scripts'))
import video_generate as video

class PollingTests(unittest.TestCase):
    def invoke(self, args):
        with patch.object(sys, 'argv', ['video_generate.py'] + args), contextlib.redirect_stdout(io.StringIO()) as out:
            code = video.main()
        return code, [json.loads(line) for line in out.getvalue().splitlines()]

    def test_generate_submits_once_then_polls_original_id(self):
        task = 'task/with+characters'
        responses = [{'output': {'task_id': task, 'task_status': status}} for status in ('PENDING', 'RUNNING')]
        responses.append({'output': {'task_status': 'SUCCEEDED', 'video_url': 'https://example.com/original.mp4', 'yoooclaw_media_url': 'https://example.com/relay'}})
        with patch.object(video.client, 'request', side_effect=responses) as request, patch.object(video.time, 'sleep'):
            code, output = self.invoke(['generate', '--seconds', '5', '--resolution', '480P', '--prompt', 'cat', '--confirmed'])
        self.assertEqual(code, 0)
        self.assertEqual([c.args[0] for c in request.call_args_list], ['POST', 'GET', 'GET'])
        self.assertTrue(all(c.args[1].endswith('task%2Fwith%2Bcharacters') for c in request.call_args_list[1:]))
        self.assertEqual(output[-1]['video_url'], 'https://example.com/original.mp4')
        self.assertEqual(output[0]['task_id'], task)

    def test_default_wait_and_zero_wait(self):
        for args in (['query', 't'], ['generate', '--seconds', '5', '--resolution', '480P', '--prompt', 'cat', '--confirmed']):
            with patch.object(video, 'wait_for_task', return_value=0) as wait, patch.object(video.client, 'request', return_value={'output': {'task_id': 't', 'task_status': 'PENDING'}}):
                self.assertEqual(self.invoke(args)[0], 0)
                self.assertEqual(wait.call_args.args[1], 1200)
        with patch.object(video.client, 'request', return_value={'output': {'task_status': 'RUNNING'}}) as request:
            self.assertEqual(self.invoke(['query', 't', '--wait', '0'])[0], 0)
            request.assert_called_once()

    def test_window_expiry_and_remaining_timeout(self):
        with patch.object(video.time, 'monotonic', side_effect=[0, 0, 2, 3]), patch.object(video.time, 'sleep') as sleep, patch.object(video.client, 'request', return_value={'output': {'task_status': 'RUNNING'}}) as request:
            code, out = self.invoke(['query', 't', '--wait', '3'])
        self.assertEqual(code, 0)
        request.assert_called_once()
        self.assertEqual(request.call_args.kwargs['timeout'], 3)
        sleep.assert_called_once_with(1)
        self.assertTrue(out[-1]['wait_expired'])
        self.assertEqual(out[-1]['status'], 'RUNNING')

    def test_query_error_keeps_task_and_last_status(self):
        with patch.object(video.client, 'request', side_effect=[{'output': {'task_status': 'RUNNING'}}, video.ApiError('error', http_status=503)]), patch.object(video.time, 'sleep'):
            code, out = self.invoke(['query', 'original-id'])
        self.assertEqual(code, 1)
        self.assertEqual(out[-1]['task_id'], 'original-id')
        self.assertEqual(out[-1]['last_task_status'], 'RUNNING')
        self.assertEqual(out[-1]['http_status'], 503)

    def test_terminal_failure_stops(self):
        for status in ('FAILED', 'CANCELED', 'UNKNOWN', 'UNEXPECTED'):
            with patch.object(video.client, 'request', return_value={'output': {'task_status': status}}) as request:
                self.assertEqual(self.invoke(['query', 't'])[0], 1)
                request.assert_called_once()

    def test_failure_details_and_immediate_terminal_response(self):
        for command in ('query', 'generate'):
            for status in ('FAILED', 'CANCELED', 'SUCCEEDED'):
                args = ['query', 'task-id'] if command == 'query' else ['generate', '--seconds', '5', '--resolution', '480P', '--prompt', 'cat', '--confirmed']
                response = {'request_id': 'req-id', 'output': {'task_id': 'task-id', 'task_status': status,
                    'code': 'InvalidImage', 'message': 'image too small',
                    'video_url': 'https://example.com/original.mp4'}}
                with self.subTest(command=command, status=status), patch.object(video.client, 'request', return_value=response) as request:
                    code, out = self.invoke(args)
                request.assert_called_once()
                self.assertEqual(code, 0 if status == 'SUCCEEDED' else 1)
                self.assertEqual(out[0]['request_id'], 'req-id')
                if status == 'SUCCEEDED':
                    self.assertEqual(out[0]['video_url'], 'https://example.com/original.mp4')
                    self.assertNotIn('message', out[0])
                else:
                    self.assertEqual(out[0]['code'], 'InvalidImage')
                    self.assertEqual(out[0]['message'], 'image too small')

    def test_task_failure_diagnostics_sanitized_with_request_key(self):
        response = {'request_id': 'req test-secret', 'output': {'task_status': 'FAILED',
            'code': 'InvalidImage', 'message': 'test-secret data:image/png;base64,PRIVATE ' + 'x'*3000}}
        opener = MagicMock()
        opener.open.return_value.__enter__.return_value = io.StringIO(json.dumps(response))
        with patch.object(video.client, 'load_api_key', return_value='test-secret'), patch.object(video.client.urllib.request, 'build_opener', return_value=opener):
            code, out = self.invoke(['query', 'task-id'])
        self.assertEqual(code, 1)
        self.assertNotIn('test-secret', str(out))
        self.assertNotIn('PRIVATE', str(out))
        self.assertLessEqual(len(out[0]['message']), 2048)
        self.assertEqual(out[0]['task_id'], 'task-id')

    def test_invalid_wait_does_not_request(self):
        for value in ('-1', '0.5', 'bad'):
            with patch.object(video.client, 'request') as request, contextlib.redirect_stderr(io.StringIO()):
                with self.assertRaises(SystemExit):
                    self.invoke(['query', 't', '--wait', value])
                request.assert_not_called()

    def test_request_timeout_override_preserves_defaults(self):
        for method, path, override, expected in [('POST', '/images/generations', {}, 300), ('GET', '/videos/tasks/t', {}, 60), ('GET', '/videos/tasks/t', {'timeout': 2.5}, 2.5)]:
            opener = MagicMock()
            opener.open.return_value.__enter__.return_value = io.StringIO('{}')
            with patch.object(video.client, 'load_api_key', return_value='test-key'), patch.object(video.client.urllib.request, 'build_opener', return_value=opener):
                video.client.request(method, path, **override)
            self.assertEqual(opener.open.call_args.kwargs['timeout'], expected)

if __name__ == '__main__':
    unittest.main()
