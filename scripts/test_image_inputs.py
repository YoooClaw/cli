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


# 结果保存（下载）由 test_media_link_guard.py 覆盖；这里隔离网络。
_SAVE = patch.object(image.client, 'save_results', side_effect=lambda kind, urls, prompt, save_dir: {'files': [], 'reply_markdown': '\n'.join('[x](' + u + ')' for u in urls)})
def setUpModule(): _SAVE.start()
def tearDownModule(): _SAVE.stop()

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
            self.assertEqual(self.invoke(['--count', '4'], 'estimate'), 0)
            self.assertIn('n=4', request.call_args.args[1])

    def test_estimate_total_and_deprecated_n(self):
        with patch.object(image.client, 'request', return_value={'credits': 200}) as request:
            self.assertEqual(self.invoke(['--count', '8'], 'estimate'), 0)
            request.assert_called_once()
            self.assertIn('n=8', request.call_args.args[1])
        for extra in (['--n', '2'], ['--n=2'], ['--n'], ['--count', '3', '--n', '2']):
            with self.subTest(extra=extra), patch.object(image.client, 'request') as request, contextlib.redirect_stderr(io.StringIO()) as output:
                with self.assertRaises(SystemExit) as caught:
                    self.invoke(extra, 'estimate')
                self.assertEqual(caught.exception.code, 2)
                self.assertIn('--count', output.getvalue())
                self.assertIn('总张数', output.getvalue())
                self.assertIn('不是提交次数', output.getvalue())
                request.assert_not_called()
        for extra in (['--count', '0'], ['--count', '2.5'], ['--cou', '2']):
            with patch.object(image.client, 'request') as request, contextlib.redirect_stderr(io.StringIO()):
                with self.assertRaises(SystemExit):
                    self.invoke(extra, 'estimate')
                request.assert_not_called()

    def test_verified_extension_combination(self):
        with patch.object(image.client, 'request', return_value={'data': [{'url': URL}]}) as request:
            self.assertEqual(self.invoke(['--image', URL, '--size', '1K',
                '--negative-prompt', '模糊，畸变，文字', '--seed', '123456',
                '--prompt-extend', 'false', '--watermark', 'false']), 0)
        body = request.call_args.args[2]
        self.assertEqual(body['size'], '1K')
        self.assertEqual(body['seed'], 123456)
        self.assertEqual(body['negative_prompt'], '模糊，畸变，文字')
        self.assertIs(body['prompt_extend'], False)
        self.assertIs(body['watermark'], False)

    def test_invalid_inputs_no_request(self):
        for extra in [
            ['--image', URL, '--size', '4K'],
            ['--image', URL, '--size', '1024x1024'], ['--image', URL, '--size', '1024*1024'], ['--image', URL, '--negative-prompt', ' '],
            ['--image', URL, '--prompt', 'x'*5001], ['--image', ''],
            ['--image', 'data:image/png;base64,invalid!'], ['--image', 'ftp://example.com/a.png'],
            ['--image', 'https://user:password@example.com/a.png'],
            ['--image', '/does-not-exist/image.png'], ['--seed', '123456'],
            ['--image', URL]*10,
        ]:
            with self.subTest(extra=extra), patch.object(image.client, 'request') as request:
                self.assertEqual(self.invoke(extra), 1)
                request.assert_not_called()

    def test_size_by_tier_and_input_mode(self):
        for tier in ('standard', 'professional'):
            for has_image in (False, True):
                for size in ('1K', '2K', '4K', '1024x1024'):
                    extra = ['--tier', tier, '--size', size]
                    if has_image:
                        extra += ['--image', URL]
                    allowed = size in ('1K', '2K') or (size == '4K' and tier == 'professional' and not has_image)
                    with self.subTest(tier=tier, image=has_image, size=size), patch.object(image.client, 'request', return_value={'data': [{'url': URL}]}) as request:
                        self.assertEqual(self.invoke(extra), 0 if allowed else 1)
                        if allowed:
                            request.assert_called_once()
                            payload = request.call_args.args[2]
                            self.assertEqual(payload['size'], size)
                            self.assertEqual('image' in payload, has_image)
                            self.assertEqual(payload['model'], image.MODELS[tier])
                        else:
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
