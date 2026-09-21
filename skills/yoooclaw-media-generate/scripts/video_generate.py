#!/usr/bin/env python3
import argparse
import sys
import client
from client import ApiError, emit, positive
import time
import urllib.parse

MODEL = "wan3.0-video-prime"

def video_seconds(value):
    try:
        seconds = int(value)
    except (ValueError, TypeError):
        raise argparse.ArgumentTypeError("视频时长必须使用整数秒（例如 2、5、10），不支持小数或其他格式；请用户重新选择 2–30 的整数秒并确认后再生成，不要自动取整。") from None
    if not 2 <= seconds <= 30:
        raise argparse.ArgumentTypeError("不支持该视频时长，仅支持 2–30 的整数秒；请用户重新选择并确认后再生成。")
    return seconds

def reference_url(value):
    try:
        parsed = urllib.parse.urlsplit(value)
        if (parsed.scheme not in ("https", "http") or not parsed.hostname
                or parsed.username or parsed.password or parsed.fragment
                or any(c.isspace() for c in value)):
            raise ValueError()
        parsed.port
    except (ValueError, TypeError):
        raise ApiError("参考图片必须是服务端可访问的 HTTP(S) URL；不支持本地路径或 data URL。") from None
    return value


def video_input(args):
    image = getattr(args, "image_url", None)
    first = getattr(args, "first_frame_url", None)
    last = getattr(args, "last_frame_url", None)
    if image and (first or last):
        raise ApiError("参考图模式与首尾帧模式不能同时使用。")
    if last and not first:
        raise ApiError("提供尾帧时必须同时提供首帧。")
    result = {"prompt": args.prompt}
    for field, value in (("img_url", image), ("first_frame_url", first), ("last_frame_url", last)):
        if value is not None:
            result[field] = reference_url(value)
    return result


def query(task_id):
    result = client.request("GET", "/videos/tasks/" + urllib.parse.quote(task_id, safe=""))
    output = result.get("output")
    if not isinstance(output, dict) or not output.get("task_status"):
        raise ApiError("任务查询响应缺少 output.task_status；保留任务 ID，核实接口契约。")
    status = str(output["task_status"]).upper()
    item = {"task_id": task_id, "status": status}
    if status == "SUCCEEDED":
        item["video_url"] = client.media_url(output, "video_url")
    emit(item)
    return status

def run(args):
    if args.command == "estimate":
        return client.estimate({"type": "video", "model": MODEL, "resolution": args.resolution, "seconds": args.seconds}, args.method)
    if args.command == "query":
        deadline = time.monotonic() + args.wait
        while True:
            status = query(args.task_id)
            if status in ("FAILED", "CANCELED", "CANCELLED", "UNKNOWN"):
                return 1
            if status == "SUCCEEDED" or status not in ("PENDING", "RUNNING", "QUEUED"):
                return 0
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                return 0
            time.sleep(min(10, remaining))
    client.check_generation(args)
    payload = {"model": MODEL, "input": video_input(args), "parameters": {"duration": args.seconds, "resolution": args.resolution}}
    result = client.request("POST", "/videos/generations", payload)
    output = result.get("output", {})
    task_id = output.get("task_id") if isinstance(output, dict) else None
    if not task_id:
        raise ApiError("响应缺少任务 ID；不要重复提交，请核实服务端任务记录。")
    emit({"task_id": task_id, "status": output.get("task_status", "PENDING")})
    return 0

def main():
    parser = argparse.ArgumentParser(description="视频生成与任务查询")
    commands = parser.add_subparsers(dest="command", required=True)
    est = commands.add_parser("estimate", help="查询预计积分")
    gen = commands.add_parser("generate", help="提交一次视频生成任务")
    for command in (est, gen):
        command.add_argument("--seconds", type=video_seconds, required=True, help="视频时长，2–30 的整数秒")
        command.add_argument("--resolution", type=str.upper, choices=("480P", "720P", "1080P"), required=True)
    est.add_argument("--method", choices=("GET", "POST"), default="GET")
    gen.add_argument("--prompt", "-p", required=True)
    gen.add_argument("--image-url", help="参考图片 HTTP(S) URL，与文字一起生成视频")
    gen.add_argument("--first-frame-url", help="首帧图片 HTTP(S) URL")
    gen.add_argument("--last-frame-url", help="尾帧图片 HTTP(S) URL，需同时提供首帧")
    gen.add_argument("--confirmed", action="store_true")
    query_parser = commands.add_parser("query", help="查询已有任务")
    query_parser.add_argument("task_id")
    query_parser.add_argument("--wait", type=positive, default=0, help="最多等待秒数，每 10 秒查询")
    return client.execute(run, parser.parse_args())

if __name__ == "__main__":
    sys.exit(main())
