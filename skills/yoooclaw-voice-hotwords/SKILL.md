---
name: yoooclaw-voice-hotwords
description: 管理 YoooClaw 云端语音识别热词。用户要查看词库、从指定内容提取候选热词、添加或删除热词、调整已有词的识别权重时使用。
---

# YoooClaw 语音识别热词

用 `yoooclaw voice hotwords` 操作当前环境中默认 API Key 所属账号的词库。如果用户指定了环境，先用 `yoooclaw env show` 核对。

## 查看词库

运行 `yoooclaw voice hotwords list --format json`，从 `items` 读取 `text` 和 `weight`。有下一页时，把 `next_cursor` 原样传给 `--cursor`。查某个词时，找到精确匹配即可返回。列出全部或判定词不存在时，读到省略 `next_cursor` 的末页。

## 选择词条

用户给了词文本时，保留拼写、大小写和内部空格，每个词作为一个参数传入。若要求从材料提取热词，只使用指定的来源，优先选取容易被误识别的人名、产品名、缩写和专业术语，按原文去重。需要读取指定录音的转写时，使用 `yoooclaw-context-query`。只要求推荐时，交付候选词。要求加入词库时，再提交选定的词。

## 写入与调权

| 用户意图 | 命令示例 | 服务行为 |
| --- | --- | --- |
| 加入词库 | `yoooclaw voice hotwords add "星桥" "YoooClaw Capture" --format json` | 新词默认权重为 4，可用 `--weight` 指定。已有词保留原权重。 |
| 调整已有词的权重 | `yoooclaw voice hotwords update "星桥" "YoooClaw Capture" --weight 5 --format json` | 必须显式给出新权重。缺失的词返回 404。 |
| 从词库移除 | `yoooclaw voice hotwords delete "星桥" "YoooClaw Capture" --format json` | 只传要删除的词。词已不存在也算成功。 |

权重是识别相近读音时对该词的偏好，取 1 到 5 的整数。1–2 轻微，3–4 较强，5 最强。一次调用中的词共用一个权重，目标权重不同就分组调用。

批量写入按参数顺序逐词执行。成功时读 `results`，部分失败时读 `error.results`，按词报告 `ok`、`error` 和 `not_attempted`。已成功的词会保留。遇到网络中断，用 `list` 核对受影响词的实际状态，再决定哪些词仍需处理。
