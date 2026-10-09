#!/usr/bin/env python3
import argparse
import sys
import client
from client import ApiError, emit
from image_generate import image_input
import time
import urllib.parse

MODEL = "wan3.0-video-prime"
DEFAULT_WAIT = 20 * 60
PENDING_STATUSES = ("PENDING", "RUNNING", "QUEUED")

def video_seconds(value):
    try:
        seconds = int(value)
    except (ValueError, TypeError):
        raise argparse.ArgumentTypeError("视频时长必须使用整数秒（例如 2、5、10），不支持小数或其他格式；请用户重新选择 2–30 的整数秒并确认后再生成，不要自动取整。") from None
    if not 2 <= seconds <= 30:
        raise argparse.ArgumentTypeError("不支持该视频时长，仅支持 2–30 的整数秒；请用户重新选择并确认后再生成。")
    return seconds

def video_input(args):
    image = getattr(args, "image_url", None)
    first = getattr(args, "first_frame_url", None)
    last = getattr(args, "last_frame_url", None)
    if image and (first or last):
        raise ApiError("参考图模式与首尾帧模式不能同时使用。")
    if last and not first:
        raise ApiError("提供尾帧时必须同时提供首帧。")
    result = {"prompt": args.prompt}
    media = []
    for kind, value in (("reference_image", image), ("first_frame", first), ("last_frame", last)):
        if value is not None:
            media.append({"type": kind, "url": image_input(value)})
    if media:
        result["media"] = media
    return result


def task_result(task_id, output, result):
    status = str(output.get("task_status", "PENDING")).upper()
    item = {"task_id": task_id, "status": status}
    if result.get("request_id") is not None:
        item["request_id"] = client.diagnostic_text(result["request_id"], "")
    if status == "SUCCEEDED":
        item["video_url"] = client.media_url(output, "video_url")
    elif status not in PENDING_STATUSES:
        for field in ("code", "message"):
            if output.get(field) is not None:
                item[field] = client.diagnostic_text(output[field], "")
    return item


def emit_task(item, args):
    """输出任务状态；成功时附带本地保存结果与可直接粘贴的交付文本。"""
    if item.get("video_url"):
        item.update(client.save_results("video", [item["video_url"]], getattr(args, "prompt", "") or "", args.save_dir))
    emit(item)


def query(task_id, args, timeout=60):
    result = client.request("GET", "/videos/tasks/" + urllib.parse.quote(task_id, safe=""), timeout=timeout)
    output = result.get("output")
    if not isinstance(output, dict) or not output.get("task_status"):
        raise ApiError("任务查询响应缺少 output.task_status；保留任务 ID，核实接口契约。")
    item = task_result(task_id, output, result)
    emit_task(item, args)
    return item["status"]

def wait_seconds(value):
    seconds = int(value)
    if seconds < 0:
        raise argparse.ArgumentTypeError("等待时间不能为负数；0 表示不轮询。")
    return seconds


def wait_for_task(task_id, wait, args, initial_status="UNKNOWN"):
    deadline = time.monotonic() + wait
    status = initial_status
    while True:
        remaining = deadline - time.monotonic()
        if wait > 0 and remaining <= 0:
            emit({"task_id": task_id, "status": status, "wait_expired": True,
                  "message": "已达到等待上限；保留任务 ID 续查，不要重新提交生成。"})
            return 0
        try:
            status = query(task_id, args, timeout=min(60, remaining) if wait > 0 else 60)
        except ApiError as exc:
            exc.details.update(task_id=task_id, last_task_status=status)
            raise
        if status == "SUCCEEDED":
            return 0
        if status not in PENDING_STATUSES:
            return 1
        if wait == 0:
            return 0
        remaining = deadline - time.monotonic()
        if remaining > 0:
            time.sleep(min(10, remaining))


def run(args):
    if args.command == "estimate":
        return client.estimate({"type": "video", "model": MODEL, "resolution": args.resolution, "seconds": args.seconds})
    if args.command == "query":
        return wait_for_task(args.task_id, args.wait, args)
    client.check_generation(args)
    payload = {"model": MODEL, "input": video_input(args), "parameters": {"duration": args.seconds, "resolution": args.resolution}}
    result = client.request("POST", "/videos/generations", payload)
    output = result.get("output", {})
    task_id = output.get("task_id") if isinstance(output, dict) else None
    if not task_id:
        raise ApiError("响应缺少任务 ID；不要重复提交，请核实服务端任务记录。")
    item = task_result(task_id, output, result)
    status = item["status"]
    emit_task(item, args)
    if status not in PENDING_STATUSES:
        return 0 if status == "SUCCEEDED" else 1
    if args.wait == 0:
        return 0
    return wait_for_task(task_id, args.wait, args, status)

def main():
    parser = argparse.ArgumentParser(description="视频生成与任务查询")
    commands = parser.add_subparsers(dest="command", required=True)
    est = commands.add_parser("estimate", help="查询预计积分")
    gen = commands.add_parser("generate", help="提交一次视频生成任务")
    for command in (est, gen):
        command.add_argument("--seconds", type=video_seconds, required=True, help="视频时长，2–30 的整数秒")
        command.add_argument("--resolution", type=str.upper, choices=("480P", "720P", "1080P"), required=True)
    gen.add_argument("--prompt", "-p", required=True)
    gen.add_argument("--image", "--image-url", dest="image_url", help="参考图片 HTTP(S) URL、Base64 data URL 或本地路径")
    gen.add_argument("--first-frame", "--first-frame-url", dest="first_frame_url", help="首帧图片 URL、Base64 data URL 或本地路径")
    gen.add_argument("--last-frame", "--last-frame-url", dest="last_frame_url", help="尾帧图片 URL、Base64 data URL 或本地路径，需同时提供首帧")
    gen.add_argument("--confirmed", action="store_true")
    query_parser = commands.add_parser("query", help="查询已有任务")
    query_parser.add_argument("task_id")
    for command in (gen, query_parser):
        command.add_argument("--wait", type=wait_seconds, default=DEFAULT_WAIT,
                             help="最多轮询等待秒数，默认 1200（20 分钟）；0 为仅提交/查询一次")
        command.add_argument("--save-dir", default="media-results", help="结果保存目录，默认当前工作目录下的 media-results/")
    return client.execute(run, parser.parse_args())

if __name__ == "__main__":
    sys.exit(main())
