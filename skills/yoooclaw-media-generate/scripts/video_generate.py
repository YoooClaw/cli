#!/usr/bin/env python3
import argparse
import sys
import client
from client import ApiError, emit, positive
import time
import urllib.parse

MODEL = "wan3.0-video-prime"

def video_seconds(value):
    seconds = positive(value)
    if seconds > 30:
        raise argparse.ArgumentTypeError("视频时长不能超过 30 秒")
    return seconds

def query(task_id):
    result = client.request("GET", "/videos/tasks/" + urllib.parse.quote(task_id, safe=""))
    output = result.get("output")
    if not isinstance(output, dict) or not output.get("task_status"):
        raise ApiError("任务查询响应缺少 output.task_status；保留任务 ID，核实接口契约。")
    status = str(output["task_status"]).upper()
    item = {"task_id": task_id, "status": status}
    if status == "SUCCEEDED":
        url = output.get("video_url")
        if not isinstance(url, str) or not url.startswith("https://"):
            raise ApiError("任务已成功，但未取得有效视频链接；保留任务 ID，不要重新生成。")
        item["video_url"] = url
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
    payload = {"model": MODEL, "input": {"prompt": args.prompt}, "parameters": {"duration": args.seconds, "resolution": args.resolution}}
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
        command.add_argument("--seconds", type=video_seconds, required=True, help="视频时长，1–30 的整数秒")
        command.add_argument("--resolution", type=str.upper, choices=("480P", "720P", "1080P"), required=True)
    est.add_argument("--method", choices=("GET", "POST"), default="GET")
    gen.add_argument("--prompt", "-p", required=True)
    gen.add_argument("--confirmed", action="store_true")
    query_parser = commands.add_parser("query", help="查询已有任务")
    query_parser.add_argument("task_id")
    query_parser.add_argument("--wait", type=positive, default=0, help="最多等待秒数，每 10 秒查询")
    return client.execute(run, parser.parse_args())

if __name__ == "__main__":
    sys.exit(main())
