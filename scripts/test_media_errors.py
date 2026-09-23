"""Mock-only error diagnostics tests; no network or credentials."""
import contextlib
import io
import json
import sys
import unittest
import urllib.error
from pathlib import Path
from unittest.mock import patch, MagicMock
sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'skills/yoooclaw-media-generate/scripts'))
import client

class MediaErrorTests(unittest.TestCase):
    def test_estimates_get_only_and_reject_method(self):
        import image_generate
        import video_generate
        for module, options in ((image_generate, ['--tier', 'standard', '--count', '2']),
                                (video_generate, ['--resolution', '480P', '--seconds', '5'])):
            args = ['script', 'estimate'] + options
            with patch.object(sys, 'argv', args), patch.object(client, 'request', return_value={'credits': 10}) as request, contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(module.main(), 0)
                request.assert_called_once()
                self.assertEqual(request.call_args.args[0], 'GET')
                self.assertTrue(request.call_args.args[1].startswith('/credits/estimate?'))
                self.assertEqual(len(request.call_args.args), 2)
            for method in ('POST', 'GET'):
                with patch.object(sys, 'argv', args + ['--method', method]), patch.object(client, 'request') as request, contextlib.redirect_stderr(io.StringIO()):
                    with self.assertRaises(SystemExit) as caught:
                        module.main()
                    self.assertEqual(caught.exception.code, 2)
                    request.assert_not_called()

    def test_http_error_details_and_no_retry(self):
        key = 'test-secret-key'
        body = {'error': {'code': 'InvalidSize', 'message': 'bad size ' + key,
                          'image': 'PRIVATE_IMAGE'}, 'prompt': 'PRIVATE_PROMPT'}
        failure = urllib.error.HTTPError('https://example.com', 503, 'error',
                                        {'x-request-id': 'request-123'}, io.BytesIO(json.dumps(body).encode()))
        opener = MagicMock()
        opener.open.side_effect = failure
        with patch.object(client, 'load_api_key', return_value=key), patch.object(client.urllib.request, 'build_opener', return_value=opener), contextlib.redirect_stdout(io.StringIO()) as output:
            self.assertEqual(client.execute(lambda _: client.request('POST', '/images/generations', {}), None), 1)
        result = json.loads(output.getvalue())
        self.assertEqual(result['http_status'], 503)
        self.assertEqual(result['request_id'], 'request-123')
        self.assertEqual(result['response_body']['error']['code'], 'InvalidSize')
        for private in (key, 'PRIVATE_IMAGE', 'PRIVATE_PROMPT'):
            self.assertNotIn(private, output.getvalue())
        opener.open.assert_called_once()

    def test_diagnostics_redacted_and_bounded(self):
        result = client.error_body(json.dumps({'message': 'data:image/png;base64,SECRETDATA Bearer other-token', 'input': {'prompt': 'private'}}), 'key')
        self.assertNotIn('SECRETDATA', str(result))
        self.assertNotIn('other-token', str(result))
        self.assertNotIn('private', str(result))
        self.assertLessEqual(len(client.error_body(json.dumps({'message': 'x'*20000}), 'key')['message']), 2048)
        self.assertNotIn('private', client.error_body('private HTML', 'key'))

    def test_business_error(self):
        response = MagicMock()
        response.__enter__.return_value = io.StringIO(json.dumps({'code': 'Denied', 'message': 'bad key', 'input': {'image': 'private'}}))
        opener = MagicMock()
        opener.open.return_value = response
        with patch.object(client, 'load_api_key', return_value='key'), patch.object(client.urllib.request, 'build_opener', return_value=opener):
            with self.assertRaises(client.ApiError) as caught:
                client.request('GET', '/credits/estimate')
        self.assertEqual(caught.exception.details['response_body']['code'], 'Denied')
        self.assertNotIn('private', str(caught.exception.details))
        self.assertNotIn('bad key', str(caught.exception.details))

    def test_large_http_body_is_marked_truncated(self):
        failure = urllib.error.HTTPError('https://example.com', 503, 'error', {}, io.BytesIO(b'x'*20000))
        opener = MagicMock()
        opener.open.side_effect = failure
        with patch.object(client, 'load_api_key', return_value='key'), patch.object(client.urllib.request, 'build_opener', return_value=opener):
            with self.assertRaises(client.ApiError) as caught:
                client.request('GET', '/test')
        self.assertTrue(caught.exception.details['response_body_truncated'])

if __name__ == '__main__':
    unittest.main()
