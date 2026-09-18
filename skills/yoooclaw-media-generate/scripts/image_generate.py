#!/usr/bin/env python3
import argparse
import sys
import client
from client import ApiError, emit, positive

MODELS = {"standard": "wan2.7-image", "professional": "wan2.7-image-pro"}

def run(args):
    if args.command == "estimate":
        return client.estimate({"type": "image", "model": MODELS[args.tier], "n": args.n}, args.method)
    client.check_generation(args)
    payload = {"model": MODELS[args.tier], "prompt": args.prompt}
    result = client.request("POST", "/images/generations", payload)
    data = result.get("data")
    if not isinstance(data, list) or not data:
        raise ApiError("响应缺少图片结果，请核实接口响应；不要自动重复生成。")
    images = [x["url"] for x in data if isinstance(x, dict) and isinstance(x.get("url"), str) and x["url"].startswith("https://")]
    if len(images) != len(data):
        raise ApiError("返回图片格式尚未支持，请核实结果；不要自动重复生成。")
    emit({"status": "success", "images": images})
    return 0

def main():
    parser = argparse.ArgumentParser(description="图片生成服务")
    commands = parser.add_subparsers(dest="command", required=True)
    est = commands.add_parser("estimate", help="查询预计积分")
    gen = commands.add_parser("generate", help="生成一张图片")
    for command in (est, gen):
        command.add_argument("--tier", choices=tuple(MODELS), required=True)
    est.add_argument("--method", choices=("GET", "POST"), default="GET")
    est.add_argument("--n", type=positive, default=1, help="估算张数；生成命令每次一张")
    gen.add_argument("--prompt", "-p", required=True)
    gen.add_argument("--confirmed", action="store_true")
    return client.execute(run, parser.parse_args())

if __name__ == "__main__":
    sys.exit(main())
