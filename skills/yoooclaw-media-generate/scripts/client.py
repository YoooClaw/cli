#!/usr/bin/env python3
"""Shared authentication, HTTP requests and credit estimation."""
import argparse
import json
from pathlib import Path
import sys
import urllib.error
import urllib.parse
import urllib.request

BASE = "https://openclaw-service-test.yoooclaw.com/model-proxy/v1"

class ApiError(Exception):
    pass

class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None

def load_api_key():
    config_path = Path.home() / ".config" / "yoooclaw" / "credentials"
    try:
        contents = config_path.read_text(encoding="utf-8")
    except (OSError, UnicodeError):
        raise ApiError("无法从 ~/.config/yoooclaw/credentials 读取 MODEL_PROXY_API_KEY，请检查配置。") from None
    key = None
    for line in contents.splitlines():
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        name, separator, value = line.partition("=")
        if separator and name.strip() == "MODEL_PROXY_API_KEY":
            key = value.strip()
            if key.startswith(("'", '"')):
                if len(key) < 2 or key[-1] != key[0]:
                    raise ApiError("MODEL_PROXY_API_KEY 的引号未闭合，请检查凭证文件。")
                key = key[1:-1]
    if key is None or not key.strip() or any(c in key for c in "\r\n"):
        raise ApiError("credentials 中的 MODEL_PROXY_API_KEY 必须是非空字符串且不能包含换行。")
    return key.strip()

def request(method, path, payload=None):
    key = load_api_key()
    headers = {"Authorization": "Bearer " + key, "Content-Type": "application/json"}
    body = json.dumps(payload, ensure_ascii=False).encode() if payload is not None else None
    req = urllib.request.Request(BASE + path, data=body, headers=headers, method=method)
    timeout = 300 if method == "POST" and path == "/images/generations" else 60
    try:
        with urllib.request.build_opener(NoRedirect).open(req, timeout=timeout) as response:
            result = json.load(response)
    except urllib.error.HTTPError as exc:
        # Do not print server bodies: they may contain credentials or internal model names.
        raise ApiError(f"服务返回 HTTP {exc.code}。未自动重试；生成请求可能已受理，请先核实。") from None
    except (urllib.error.URLError, TimeoutError, OSError):
        raise ApiError("请求未完成，结果不确定。不要重复提交生成请求；已有任务请使用任务 ID 查询。") from None
    except (ValueError, UnicodeError):
        raise ApiError("服务返回无法解析的响应。不要自动重新提交生成请求。") from None
    if not isinstance(result, dict):
        raise ApiError("服务返回的 JSON 结构不符合预期。")
    if result.get("error") or result.get("code") not in (None, 0, "0", 200, "200"):
        raise ApiError("服务返回业务错误；请核实鉴权、参数或余额后再继续。")
    return result

def positive(value):
    result = int(value)
    if result <= 0:
        raise argparse.ArgumentTypeError("必须是正整数")
    return result

def emit(value):
    print(json.dumps(value, ensure_ascii=False), flush=True)

def estimate(payload, method="GET"):
    if method == "GET":
        result = request("GET", "/credits/estimate?" + urllib.parse.urlencode(payload))
    else:
        result = request("POST", "/credits/estimate", payload)
    emit({"estimate_response": result})

def execute(run, args):
    try:
        return run(args) or 0
    except (ApiError, OSError, ValueError) as exc:
        message = str(exc) if isinstance(exc, ApiError) else "本地配置读取失败，请检查凭证文件格式和权限。"
        emit({"status": "error", "message": message})
        return 1

def check_generation(args):
    if not args.confirmed:
        raise ApiError("先向用户展示规格与本次积分，获得选择及授权后再传 --confirmed。")
    if not args.prompt.strip():
        raise ApiError("生成描述不能为空。")
