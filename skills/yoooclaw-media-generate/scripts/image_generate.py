#!/usr/bin/env python3
import argparse
import sys
import base64
import binascii
import re
from pathlib import Path
import urllib.parse
import client
from client import ApiError, emit, positive

MODELS = {"standard": "wan2.7-image", "professional": "wan2.7-image-pro"}

MAX_IMAGE_BYTES = 20 * 1024 * 1024
MIMES = {"image/jpeg", "image/jpg", "image/png", "image/bmp", "image/webp"}

def image_count(value):
    n = positive(value)
    if n > 4:
        raise argparse.ArgumentTypeError("每次生成 1–4 张图片")
    return n

def boolean_value(value):
    if value not in ("true", "false"):
        raise argparse.ArgumentTypeError("必须是 true 或 false")
    return value == "true"

def seed_value(value):
    seed = int(value)
    if not 0 <= seed <= 2147483647:
        raise argparse.ArgumentTypeError("seed 必须在 0–2147483647 之间")
    return seed

def image_mime(data):
    if data.startswith(b"\xff\xd8\xff"):
        return "image/jpeg"
    if data.startswith(b"\x89PNG\r\n\x1a\n"):
        return "image/png"
    if data.startswith(b"BM"):
        return "image/bmp"
    if data.startswith(b"RIFF") and data[8:12] == b"WEBP":
        return "image/webp"
    raise ApiError("参考图片必须为 JPEG、PNG、BMP 或 WEBP 格式。")

def image_input(value):
    if not value or not value.strip():
        raise ApiError("参考图片不能为空。")
    if value.startswith("data:"):
        header, sep, encoded = value.partition(",")
        mime = header[5:].removesuffix(";base64")
        if not sep or not header.endswith(";base64") or mime not in MIMES:
            raise ApiError("参考图片 data URL 格式不正确。")
        if len(encoded) > ((MAX_IMAGE_BYTES + 2) // 3) * 4:
            raise ApiError("每张参考图片不能超过 20 MB。")
        try:
            data = base64.b64decode(encoded, validate=True)
        except (ValueError, binascii.Error):
            raise ApiError("参考图片 Base64 编码无效。") from None
        detected = image_mime(data)
        if detected != ("image/jpeg" if mime == "image/jpg" else mime):
            raise ApiError("参考图片 MIME 类型与内容不一致。")
        if len(data) > MAX_IMAGE_BYTES:
            raise ApiError("每张参考图片不能超过 20 MB。")
        return value
    if "://" in value:
        try:
            url = urllib.parse.urlsplit(value)
            if (url.scheme not in ("http", "https") or not url.hostname or url.username
                    or url.password or url.fragment or any(c.isspace() for c in value)):
                raise ValueError()
            url.port
        except ValueError:
            raise ApiError("参考图片链接必须是服务端可访问的 HTTP(S) URL。") from None
        return value
    try:
        with Path(value).expanduser().open("rb") as source:
            data = source.read(MAX_IMAGE_BYTES + 1)
    except OSError:
        raise ApiError("无法读取本地参考图片，请检查文件路径和权限。") from None
    if len(data) > MAX_IMAGE_BYTES:
        raise ApiError("每张参考图片不能超过 20 MB。")
    return "data:" + image_mime(data) + ";base64," + base64.b64encode(data).decode("ascii")

def generation_payload(args):
    payload = {"model": MODELS[args.tier], "prompt": args.prompt, "n": args.n}
    if args.image:
        if len(args.image) > 9:
            raise ApiError("最多提供 9 张参考图片。")
        if len(args.prompt) > 5000:
            raise ApiError("图生图编辑指令不能超过 5000 字符。")
        payload["image"] = [image_input(value) for value in args.image]
    options = ("size", "negative_prompt", "seed", "prompt_extend", "watermark")
    if not args.image and any(getattr(args, name) is not None for name in options):
        raise ApiError("这些扩展参数当前仅用于图生图，请同时提供 --image。")
    if args.size is not None:
        size = args.size
        if size not in ("1K", "2K") and not re.fullmatch(r"[1-9][0-9]*[x*][1-9][0-9]*", size):
            raise ApiError("图生图 size 仅支持 1K、2K 或宽高像素（如 1024x1024），不支持 4K。")
    if args.negative_prompt is not None and not args.negative_prompt.strip():
        raise ApiError("反向提示词不能为空。")
    for name in options:
        value = getattr(args, name)
        if value is not None:
            payload[name] = value
    return payload

def run(args):
    if args.command == "estimate":
        return client.estimate({"type": "image", "model": MODELS[args.tier], "n": args.n}, args.method)
    client.check_generation(args)
    payload = generation_payload(args)
    result = client.request("POST", "/images/generations", payload)
    data = result.get("data")
    if not isinstance(data, list) or not data:
        raise ApiError("响应缺少图片结果，请核实接口响应；不要自动重复生成。")
    images = [client.media_url(item) for item in data]
    emit({"status": "success", "images": images})
    return 0

def main():
    parser = argparse.ArgumentParser(description="图片生成服务")
    commands = parser.add_subparsers(dest="command", required=True)
    est = commands.add_parser("estimate", help="查询预计积分")
    gen = commands.add_parser("generate", help="文生图或图生图，生成 1–4 张图片")
    for command in (est, gen):
        command.add_argument("--tier", choices=tuple(MODELS), required=True)
        command.add_argument("--n", type=image_count, default=1, help="生成张数，1–4；估价与生成须一致")
    est.add_argument("--method", choices=("GET", "POST"), default="GET")
    gen.add_argument("--prompt", "-p", required=True)
    gen.add_argument("--image", action="append", help="参考图片 URL、Base64 data URL 或本地路径；可重复，最多 9 张")
    gen.add_argument("--size", help="图生图输出规格：1K、2K 或宽高像素")
    gen.add_argument("--negative-prompt", help="反向提示词")
    gen.add_argument("--seed", type=seed_value)
    for name in ("prompt-extend", "watermark"):
        gen.add_argument("--" + name, type=boolean_value,
                         metavar="true|false")
    gen.add_argument("--confirmed", action="store_true")
    return client.execute(run, parser.parse_args())

if __name__ == "__main__":
    sys.exit(main())
