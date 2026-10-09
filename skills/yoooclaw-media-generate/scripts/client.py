#!/usr/bin/env python3
"""Shared authentication, HTTP requests and credit estimation."""
import argparse
import json
import os
import re
from pathlib import Path
import sys
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request

BASE = "https://openclaw-service.yoooclaw.com/model-proxy/v1"

class ApiError(Exception):
    def __init__(self, message, **details):
        super().__init__(message)
        self.details = details


def diagnostic_text(value, key):
    text = str(value)
    if key:
        text = text.replace(key, "[REDACTED]")
    text = re.sub(r"data:[^\s\"'<>]*", "[REDACTED_IMAGE]", text, flags=re.I)
    text = re.sub(r"Bearer\s+[^\s\"'<>]+", "Bearer [REDACTED]", text, flags=re.I)
    return text[:2048]


def error_body(raw, key):
    # Only expose diagnostic fields, never echoed request objects or image arrays.
    text = raw.decode("utf-8", errors="replace") if isinstance(raw, bytes) else raw
    try:
        body = json.loads(text)
    except ValueError:
        return "非 JSON 错误响应，正文已省略。"
    def select(value):
        if not isinstance(value, dict):
            return diagnostic_text(value, key) if isinstance(value, (str, int, float)) else None
        result = {}
        for name in ("code", "message", "type", "param", "request_id", "requestId", "error"):
            if name in value:
                result[name] = select(value[name])
        return result
    return select(body)

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

def request(method, path, payload=None, *, timeout=None):
    key = load_api_key()
    headers = {"Authorization": "Bearer " + key, "Content-Type": "application/json"}
    body = json.dumps(payload, ensure_ascii=False).encode() if payload is not None else None
    req = urllib.request.Request(BASE + path, data=body, headers=headers, method=method)
    if timeout is None:
        timeout = 300 if method == "POST" and path == "/images/generations" else 60
    try:
        with urllib.request.build_opener(NoRedirect).open(req, timeout=timeout) as response:
            result = json.load(response)
    except urllib.error.HTTPError as exc:
        details = {"http_status": exc.code}
        try:
            raw = exc.read(16385)
            details["response_body"] = error_body(raw[:16384], key)
            if len(raw) > 16384:
                details["response_body_truncated"] = True
        except (OSError, ValueError):
            details["response_body_unavailable"] = True
        finally:
            exc.close()
        if exc.headers and exc.headers.get("x-request-id"):
            details["request_id"] = diagnostic_text(exc.headers.get("x-request-id"), key)
        raise ApiError(f"服务返回 HTTP {exc.code}。未自动重试。", **details) from None
    except (urllib.error.URLError, TimeoutError, OSError):
        raise ApiError("请求未完成，结果不确定。不要重复提交生成请求；已有任务请使用任务 ID 查询。") from None
    except (ValueError, UnicodeError):
        raise ApiError("服务返回无法解析的响应。不要自动重新提交生成请求。") from None
    if not isinstance(result, dict):
        raise ApiError("服务返回的 JSON 结构不符合预期。")
    if result.get("error") or result.get("code") not in (None, 0, "0", 200, "200"):
        raise ApiError("服务返回业务错误；请核实鉴权、参数或余额后再继续。", response_body=error_body(json.dumps(result, ensure_ascii=False), key))
    # Video task failures arrive as HTTP 200; sanitize their diagnostics with
    # the credential used for this request before exposing them to the caller.
    if result.get("request_id") is not None:
        result["request_id"] = diagnostic_text(result["request_id"], key)
    output = result.get("output")
    if isinstance(output, dict):
        for field in ("code", "message"):
            if output.get(field) is not None:
                output[field] = diagnostic_text(output[field], key)
    return result

def positive(value):
    result = int(value)
    if result <= 0:
        raise argparse.ArgumentTypeError("必须是正整数")
    return result

def media_url(item, field="url"):
    url = item.get(field) if isinstance(item, dict) else None
    if isinstance(url, str) and not any(c.isspace() for c in url):
        try:
            parsed = urllib.parse.urlsplit(url)
            if parsed.scheme in ("http", "https") and parsed.hostname and not parsed.username and not parsed.password:
                return url
        except ValueError:
            pass
    raise ApiError("响应缺少有效的供应商原始媒体链接；请核实原结果，不要重新生成。")

def emit(value):
    print(json.dumps(value, ensure_ascii=False), flush=True)

def estimate(payload):
    result = request("GET", "/credits/estimate?" + urllib.parse.urlencode(payload))
    emit({"estimate_response": result})


def execute(run, args):
    try:
        return run(args) or 0
    except (ApiError, OSError, ValueError) as exc:
        message = str(exc) if isinstance(exc, ApiError) else "本地配置读取失败，请检查凭证文件格式和权限。"
        emit({"status": "error", "message": message, **(exc.details if isinstance(exc, ApiError) else {})})
        return 1

def check_generation(args):
    if not args.confirmed:
        raise ApiError("先向用户展示规格与本次积分，获得选择及授权后再传 --confirmed。")
    if not args.prompt.strip():
        raise ApiError("生成描述不能为空。")


# ── 结果保存与交付文本 ──
# 由脚本下载结果并生成可直接粘贴的 Markdown，Agent 不再手写下载命令或拼接链接。

MEDIA_TYPES = {
    "image/png": ".png", "image/jpeg": ".jpg", "image/webp": ".webp", "image/bmp": ".bmp",
    "video/mp4": ".mp4", "video/quicktime": ".mov", "video/webm": ".webm",
}
DOWNLOAD_LIMIT = 1024 * 1024 * 1024


def safe_stem(text, limit=24):
    stem = re.sub(r"[^\w\u4e00-\u9fff-]+", "-", text).strip("-")
    return stem[:limit] or "media"


def download(url, kind, save_dir, stem):
    """下载单个媒体，校验类型与非空后原子落盘，返回绝对路径。不携带任何凭据。"""
    target_dir = Path(save_dir).expanduser().resolve()
    target_dir.mkdir(parents=True, exist_ok=True)
    req = urllib.request.Request(url, headers={"User-Agent": "yoooclaw-media/1"})
    with urllib.request.urlopen(req, timeout=120) as response:
        mime = (response.headers.get_content_type() or "").lower()
        if not mime.startswith(kind + "/"):
            raise ApiError("下载内容类型不是" + ("图片" if kind == "image" else "视频") + "：" + mime)
        suffix = MEDIA_TYPES.get(mime, "." + mime.split("/")[-1][:8])
        fd, tmp = tempfile.mkstemp(dir=target_dir, prefix=".download-")
        size = 0
        try:
            with os.fdopen(fd, "wb") as out:
                while True:
                    chunk = response.read(1 << 20)
                    if not chunk:
                        break
                    size += len(chunk)
                    if size > DOWNLOAD_LIMIT:
                        raise ApiError("下载内容超过 1GB 上限")
                    out.write(chunk)
            if size == 0:
                raise ApiError("下载内容为空")
            path = target_dir / (stem + suffix)
            index = 2
            while path.exists():
                path = target_dir / f"{stem}-{index}{suffix}"
                index += 1
            os.replace(tmp, path)
            return str(path)
        except BaseException:
            Path(tmp).unlink(missing_ok=True)
            raise


def reply_markdown(kind, urls, prompt):
    """每个结果一行描述性下载链接；链接原样取自接口结果。"""
    label = "图片" if kind == "image" else "视频"
    subject = re.sub(r"[\[\]()\\\r\n]+", " ", prompt).strip()
    subject = (subject[:20] + "…") if len(subject) > 20 else subject
    lines = []
    for index, url in enumerate(urls, 1):
        suffix = f" {index}" if len(urls) > 1 else ""
        text = f"{subject}{label}{suffix}（点击下载）" if subject else f"{label}{suffix}（点击下载）"
        lines.append(f"[{text}]({url})")
    return "\n".join(lines)


def save_results(kind, urls, prompt, save_dir):
    """下载全部结果到 save_dir，返回 files / download_failed / reply_markdown。"""
    stem = time.strftime("%Y%m%d-%H%M%S") + "-" + safe_stem(prompt)
    files, failed = [], 0
    for index, url in enumerate(urls, 1):
        try:
            files.append(download(url, kind, save_dir, stem if len(urls) == 1 else f"{stem}-{index}"))
        except (ApiError, OSError, urllib.error.URLError, TimeoutError, ValueError):
            failed += 1
    result = {"files": files, "reply_markdown": reply_markdown(kind, urls, prompt)}
    if failed:
        result["download_failed"] = failed
    return result
