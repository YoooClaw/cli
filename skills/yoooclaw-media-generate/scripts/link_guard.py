#!/usr/bin/env python3
"""Claude Code Stop hook：拦截与生成脚本真实输出不一致的媒体链接。

真实链接只取自本会话工具结果里生成脚本输出的 JSON（images / video_url /
yoooclaw_media_url）。最终回复里与某个真实链接高度相似但不完全相同的链接
（漏字、加字、截断、转义）视为转写错误：以 exit 2 阻止结束并把正确链接交给
Claude 重写。已因本 hook 续写过一次时不再阻止，改为给用户显示正确链接。
任何异常都放行（exit 0），hook 不能影响正常对话。
"""
import json
import re
import sys

MAX_DISTANCE_RATIO = 0.3
MIN_MARGIN_RATIO = 0.05
MEDIA_KEYS = ("images", "video_url", "yoooclaw_media_url")
URL_PATTERN = re.compile(r"https?://[^\s<>\"'`()\[\]{}　-〿＀-￯]+")
TRAILING = re.compile(r"[.,;:!?…]+$")
ELLIPSIS = re.compile(r"(…|\.\.\.|%E2%80%A6)+$", re.I)


def valid_url(value):
    return isinstance(value, str) and re.match(r"^https?://[^\s/]+", value) is not None and not re.search(r"\s", value)


def collect_object(obj, out):
    if not isinstance(obj, dict):
        return
    images = obj.get("images")
    if isinstance(images, list):
        out.extend(u for u in images if valid_url(u))
    for key in ("video_url", "yoooclaw_media_url"):
        if valid_url(obj.get(key)):
            out.append(obj[key])


def collect_text(text, out):
    if not any(key in text for key in MEDIA_KEYS):
        return
    for candidate in [text] + text.splitlines():
        candidate = candidate.strip()
        if candidate.startswith("{") and candidate.endswith("}"):
            try:
                collect_object(json.loads(candidate), out)
            except ValueError:
                pass


def extract_media_urls(value):
    out = []

    def walk(node, depth=0):
        if depth > 12 or node is None:
            return
        if isinstance(node, str):
            collect_text(node, out)
        elif isinstance(node, list):
            for item in node:
                walk(item, depth + 1)
        elif isinstance(node, dict):
            collect_object(node, out)
            for child in node.values():
                walk(child, depth + 1)

    walk(value)
    return list(dict.fromkeys(out))


def is_tool_result_entry(entry):
    """转写里的工具结果：user 消息中的 tool_result 块，或附带的 toolUseResult。"""
    if not isinstance(entry, dict) or entry.get("type") != "user":
        return False
    if "toolUseResult" in entry:
        return True
    content = (entry.get("message") or {}).get("content")
    return isinstance(content, list) and any(isinstance(b, dict) and b.get("type") == "tool_result" for b in content)


def genuine_urls_from_transcript(path):
    urls = []
    try:
        with open(path, encoding="utf-8") as transcript:
            for line in transcript:
                if not any(key in line for key in MEDIA_KEYS):
                    continue
                try:
                    entry = json.loads(line)
                except ValueError:
                    continue
                if is_tool_result_entry(entry):
                    urls.extend(extract_media_urls([entry.get("message"), entry.get("toolUseResult")]))
    except OSError:
        return []
    return list(dict.fromkeys(urls))


def levenshtein(a, b, limit):
    if abs(len(a) - len(b)) > limit:
        return limit + 1
    prev = list(range(len(b) + 1))
    for i in range(1, len(a) + 1):
        curr = [i] + [0] * len(b)
        for j in range(1, len(b) + 1):
            curr[j] = min(prev[j] + 1, curr[j - 1] + 1, prev[j - 1] + (a[i - 1] != b[j - 1]))
        if min(curr) > limit:
            return limit + 1
        prev = curr
    return prev[len(b)]


def resolve(written, genuine):
    """返回写出的链接本应对应的真实链接；与所有真实链接都不像时返回 None。"""
    if written in genuine:
        return written
    truncated = ELLIPSIS.sub("", written)
    if truncated != written and len(truncated) >= 16:
        matches = [u for u in genuine if u.startswith(truncated)]
        if len(matches) == 1:
            return matches[0]
    unescaped = re.sub(r"\\(?=[_*~\\()\[\]])", "", written)
    if unescaped in genuine:
        return unescaped
    best, best_ratio, second = None, None, float("inf")
    for url in genuine:
        longest = max(len(url), len(written))
        limit = int(longest * MAX_DISTANCE_RATIO)
        distance = levenshtein(written, url, limit)
        if distance > limit:
            continue
        ratio = distance / longest
        if best is None or ratio < best_ratio:
            second = best_ratio if best_ratio is not None else float("inf")
            best, best_ratio = url, ratio
        elif ratio < second:
            second = ratio
    if best is None or second - best_ratio < MIN_MARGIN_RATIO:
        return None
    return best


def find_mismatches(text, genuine):
    """返回 [(写出的链接, 正确链接)]，只包含写错的媒体链接。"""
    mismatches = []
    for match in URL_PATTERN.findall(text or ""):
        trailing = TRAILING.search(match)
        candidates = [match]
        if trailing and not re.fullmatch(r"(…|\.{3,})+", trailing.group()):
            candidates.insert(0, match[: trailing.start()])
        for candidate in candidates:
            correct = resolve(candidate, genuine)
            if correct:
                if correct != candidate:
                    mismatches.append((candidate, correct))
                break
    return list(dict.fromkeys(mismatches))


def main():
    try:
        payload = json.load(sys.stdin)
        genuine = genuine_urls_from_transcript(payload.get("transcript_path") or "")
        if not genuine:
            return 0
        mismatches = find_mismatches(payload.get("last_assistant_message") or "", genuine)
        if not mismatches:
            return 0
        lines = [f"- 错误：{wrong}\n  正确：{right}" for wrong, right in mismatches]
        if payload.get("stop_hook_active"):
            print(json.dumps({"systemMessage": "媒体链接校验：回复中的链接可能有误，请以以下正确链接为准：\n"
                              + "\n".join(right for _, right in mismatches)}, ensure_ascii=False))
            return 0
        print("回复中的媒体链接与生成脚本输出不一致，用户点开会失败。请重新输出完整的最终回复，"
              "把下列链接逐字替换为正确值（直接复制，不要改写、截断或转义）：\n" + "\n".join(lines),
              file=sys.stderr)
        return 2
    except Exception:  # hook 自身异常一律放行
        return 0


if __name__ == "__main__":
    sys.exit(main())
