#!/usr/bin/env python3
"""Claude hooks for media execution. State contains IDs/status only, never credentials."""
import contextlib
import io
import re
import hashlib
import json
import os
from pathlib import Path
import shlex
import sys
import time
import uuid


def state_dir(session):
    return Path.home() / ".cache/yoooclaw/media-waits" / hashlib.sha256(session.encode()).hexdigest()


class TaskState:
    def __init__(self, args):
        self.path = None
        session = os.environ.get("YOOOCLAW_MEDIA_SESSION")
        if not session or args.command == "estimate":
            return
        directory = state_dir(session)
        directory.mkdir(parents=True, exist_ok=True, mode=0o700)
        self.path = directory / (uuid.uuid4().hex + ".json")
        self.data = {"pid": os.getpid(), "started": time.time(),
                     "deadline": time.time() + args.wait + 120,
                     "task_id": getattr(args, "task_id", None), "phase": "active"}
        self.update()

    def update(self, **fields):
        if self.path is None:
            return
        self.data.update(fields)
        temporary = self.path.with_suffix(".tmp")
        temporary.write_text(json.dumps(self.data), encoding="utf-8")
        temporary.replace(self.path)


def media_command(command, cwd=None):
    """Positive match of direct, valid invocations of this installed video script.

    This is a workflow guard, not a general shell sandbox. Do not guess at
    commands embedded in echo, Python source, shell wrappers or other scripts.
    The actual CLI parser decides whether argv is a generation/query request.
    """
    try:
        words = shlex.split(command)
    except ValueError:
        return None
    background = False
    while words and re.fullmatch(r"[A-Za-z_][A-Za-z0-9_]*=.*", words[0], re.S):
        words.pop(0)
    if words and words[0] == "env":
        words.pop(0)
        while words and re.fullmatch(r"[A-Za-z_][A-Za-z0-9_]*=.*", words[0], re.S):
            words.pop(0)
    if words and words[0] == "nohup":
        background = True
        words.pop(0)
    if not words:
        return None
    if re.fullmatch(r"python(?:3(?:\.\d+)?)?", Path(words[0]).name):
        words.pop(0)
    if not words:
        return None
    candidate = Path(words.pop(0)).expanduser()
    if not candidate.is_absolute():
        candidate = Path(cwd or os.getcwd()) / candidate
    if candidate.resolve() != Path(__file__).with_name("video_generate.py").resolve():
        return None
    # Share argparse with the script: --help and invalid CLI syntax never
    # represent an executable media request and therefore are not intercepted.
    from video_generate import build_parser
    variants = [(words, background)]
    if words and words[-1] == "&" and command.rstrip().endswith("&"):
        variants.append((words[:-1], True))
    for argv, is_background in variants:
        try:
            with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
                args = build_parser().parse_args(argv)
        except SystemExit:
            continue
        if args.command in ("generate", "query"):
            return is_background
    return None


def pre_tool(payload):
    inp = payload.get("tool_input", {})
    command = inp.get("command", "")
    if payload.get("tool_name") != "Bash":
        return None
    background = media_command(command, payload.get("cwd"))
    if background is None:
        return None
    if inp.get("run_in_background") or background:
        reason = "视频任务禁止主动后台执行。移除 run_in_background、nohup 或 &，前台等待原进程；不要重复提交已创建的任务。"
    elif inp.get("timeout") != 1200000:
        reason = "执行视频脚本必须设置 Bash timeout: 1200000（20 分钟）。请修改工具参数后重试；本次命令尚未执行。"
    else:
        session = payload.get("session_id")
        if not session:
            return None
        updated = dict(inp)
        updated["command"] = "export YOOOCLAW_MEDIA_SESSION=" + shlex.quote(session) + "; " + command
        return {"hookSpecificOutput": {"hookEventName": "PreToolUse", "updatedInput": updated}}
    return {"hookSpecificOutput": {"hookEventName": "PreToolUse", "permissionDecision": "deny",
                                   "permissionDecisionReason": reason}}


def stop(payload):
    session = payload.get("session_id")
    if not session:
        return None
    # A successful continuation/query supersedes an older interrupted waiter.
    latest = {}
    for path in state_dir(session).glob("*.json"):
        try:
            data = json.loads(path.read_text())
        except (OSError, ValueError):
            continue
        key = data.get("task_id") or path.name
        if data.get("started", 0) >= latest.get(key, {}).get("started", 0):
            latest[key] = data
    pending, expired = [], []
    for data in latest.values():
        phase = data.get("phase")
        if phase not in ("active", "ended"):
            continue
        if phase == "ended" and (data.get("exit_code") != 0 or
                                  data.get("status") not in ("PENDING", "RUNNING", "QUEUED")):
            continue
        task = data.get("task_id") or "尚未取得任务 ID（先核实提交结果，不重新提交）"
        if time.time() >= data.get("deadline", 0):
            expired.append(str(task))
            continue
        alive = phase == "active"
        if alive:
            try:
                os.kill(data["pid"], 0)
            except (OSError, KeyError, TypeError):
                alive = False
        pending.append(str(task) + ("：原进程 PID " + str(data["pid"]) + " 仍在运行，必须持续等待原执行任务，不能另开 query"
                                    if alive else "：原等待进程已结束，必须用原任务 ID 执行 query 并等待，不能重新 generate"))
    if pending:
        # stop_hook_active is not task completion. Never release a live task merely
        # because this hook has already requested a continuation.
        return {"decision": "block", "reason": "视频任务尚未完成：\n" + "\n".join(pending) +
                "。不要只报告进度就结束本轮，也不能承诺结束后自动通知。请等待完成、明确失败或等待上限到达。"}
    if expired:
        return {"systemMessage": "视频等待已达到上限，请如实报告最后状态和任务 ID，不声称生成失败或后台自动通知：" + "、".join(expired)}
    return None


def main():
    try:
        payload = json.load(sys.stdin)
        result = pre_tool(payload) if payload.get("hook_event_name") == "PreToolUse" else stop(payload)
        if result:
            print(json.dumps(result, ensure_ascii=False))
    except Exception as exc:
        print(json.dumps({"systemMessage": "媒体等待 Hook 校验异常：" + type(exc).__name__}, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    sys.exit(main())
