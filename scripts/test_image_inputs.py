"""Mock-only image generation tests: python3 -B scripts/test_image_inputs.py."""
import base64
import contextlib
import io
import sys
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'skills/yoooclaw-media-generate/scripts'))
import image_generate as image

PNG = base64.b64decode('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+a0n8AAAAASUVORK5CYII=')
URL = 'https://example.com/a.png?Signature=a%2Bb%3D&Expires=123'

class ImageInputsTests(unittest.TestCase):
    def invoke(self, extra, command='generate'):
        args = ['image_generate.py', command, '--tier', 'professional']
        if command == 'generate':
            args += ['--prompt', '更换背景', '--confirmed']
        with patch.object(sys, 'argv', args + extra), contextlib.redirect_stdout(io.StringIO()):
            return image.main()

    def test_edit_mapping_and_response(self):
        with tempfile.TemporaryDirectory() as folder:
            path = Path(folder) / 'input.png'; path.write_bytes(PNG)
            with patch.object(image.client, 'request', return_value={'data': [
                {'yoooclaw_media_url': 'https://example.com/media/1', 'url': URL}, {'url': URL}
            ]}) as request:
                self.assertEqual(self.invoke(['--image', URL, '--image', str(path), '--n', '2',
                    '--size', '2K', '--negative-prompt', '模糊', '--seed', '2147483647',
                    '--prompt-extend', 'true', '--watermark', 'false']), 0)
                request.assert_called_once()
                method, route, payload = request.call_args.args
                self.assertEqual((method, route), ('POST', '/images/generations'))
                self.assertEqual(payload, {'model': 'wan2.7-image-pro', 'prompt': '更换背景', 'n': 2,
                    'image': [URL, 'data:image/png;base64,' + base64.b64encode(PNG).decode()],
                    'size': '2K', 'negative_prompt': '模糊', 'seed': 2147483647,
                    'prompt_extend': True, 'watermark': False})

    def test_text_generation_and_estimate_count(self):
        with patch.object(image.client, 'request', return_value={'data': [{'url': URL}]}) as request:
            self.assertEqual(self.invoke(['--n', '4']), 0)
            self.assertNotIn('image', request.call_args.args[2])
            self.assertEqual(request.call_args.args[2]['n'], 4)
        with patch.object(image.client, 'request', return_value={'credits': 101}) as request:
            self.assertEqual(self.invoke(['--n', '4'], 'estimate'), 0)
            self.assertIn('n=4', request.call_args.args[1])

    def test_invalid_inputs_no_request(self):
        for extra in [
            ['--image', URL, '--size', '4K'], ['--image', URL, '--negative-prompt', ' '],
            ['--image', URL, '--prompt', 'x'*5001], ['--image', ''],
            ['--image', 'data:image/png;base64,invalid!'], ['--image', 'ftp://example.com/a.png'],
            ['--image', 'https://user:password@example.com/a.png'],
            ['--image', '/does-not-exist/image.png'], ['--size', '2K'],
            ['--image', URL]*10,
        ]:
            with self.subTest(extra=extra), patch.object(image.client, 'request') as request:
                self.assertEqual(self.invoke(extra), 1)
                request.assert_not_called()

    def test_cli_ranges(self):
        for extra in [['--n', '0'], ['--n', '5'], ['--seed', '-1'], ['--seed', '2147483648'], ['--watermark', 'maybe']]:
            with patch.object(image.client, 'request') as request, contextlib.redirect_stderr(io.StringIO()):
                with self.assertRaises(SystemExit) as caught:
                    self.invoke(extra)
                self.assertEqual(caught.exception.code, 2)
                request.assert_not_called()

    def test_data_url_and_limits(self):
        data_url = 'data:image/png;base64,' + base64.b64encode(PNG).decode()
        self.assertEqual(image.image_input(data_url), data_url)
        with patch.object(image, 'MAX_IMAGE_BYTES', len(PNG)-1):
            with self.assertRaises(image.ApiError): image.image_input(data_url)
            with tempfile.TemporaryDirectory() as folder:
                path = Path(folder) / 'input.png'; path.write_bytes(PNG)
                with self.assertRaises(image.ApiError): image.image_input(str(path))

    def test_requires_confirmation(self):
        args = ['image_generate.py', 'generate', '--tier', 'standard', '--prompt', '修改', '--image', URL]
        with patch.object(sys, 'argv', args), patch.object(image.client, 'request') as request, contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(image.main(), 1)
            request.assert_not_called()

if __name__ == '__main__':
    unittest.main()
