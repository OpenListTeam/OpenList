#!/usr/bin/env python3
"""
GitHub Issue 分诊机器人（Triage Bot）

流程：
1. 读取触发事件（Issue 标题、正文、作者、标签、评论）。
2. 读取系统提示词 system-prompt.md。
3. 调用自定义 LLM API（OpenAI 兼容 /chat/completions 协议）获取结构化决策。
4. 根据决策 JSON 执行操作：改标题、打标签、评论、关闭、锁定、关联等。

依赖：
- gh CLI（GitHub Actions 预装并已通过 GITHUB_TOKEN 认证）
- Python 3 标准库（无第三方依赖）

需要的环境变量（由 workflow 注入）：
- GITHUB_TOKEN / GITHUB_REPOSITORY / GITHUB_EVENT_PATH（GitHub 自动提供）
- CUSTOM_API_BASE_URL / CUSTOM_API_KEY / CUSTOM_API_MODEL
- SYSTEM_PROMPT_FILE
"""

import json
import os
import subprocess
import sys
import urllib.request
import urllib.error


# ---------------------------------------------------------------------------
# 工具函数
# ---------------------------------------------------------------------------

def log(msg: str) -> None:
    print(f"[triage] {msg}", flush=True)


def gh(*args: str, check: bool = True) -> str:
    """调用 gh CLI，返回 stdout（str）。"""
    cmd = ["gh"] + list(args)
    log("gh " + " ".join(cmd))
    proc = subprocess.run(cmd, capture_output=True, text=True)
    if check and proc.returncode != 0:
        log(f"gh 命令失败: {proc.stderr.strip()}")
        raise RuntimeError(proc.stderr.strip())
    return proc.stdout.strip()


def gh_json(*args: str):
    out = gh(*args)
    return json.loads(out) if out else None


def read_env(name: str, default: str = "") -> str:
    val = os.environ.get(name, default)
    if not val:
        log(f"缺少环境变量: {name}")
        raise RuntimeError(f"Missing env: {name}")
    return val


# ---------------------------------------------------------------------------
# 1. 读取系统提示词
# ---------------------------------------------------------------------------

def load_system_prompt(path: str) -> str:
    if not os.path.isfile(path):
        raise RuntimeError(f"系统提示词文件不存在: {path}")
    with open(path, "r", encoding="utf-8") as f:
        return f.read()


# ---------------------------------------------------------------------------
# 2. 读取事件与 Issue 上下文
# ---------------------------------------------------------------------------

def load_event() -> dict:
    path = os.environ.get("GITHUB_EVENT_PATH", "")
    if not path or not os.path.isfile(path):
        raise RuntimeError("无法读取 GitHub 事件文件")
    with open(path, "r", encoding="utf-8") as f:
        return json.load(f)


def get_issue_comments(number: int) -> str:
    """拉取 Issue 现有评论，作为多轮对话上下文。"""
    repo = os.environ.get("GITHUB_REPOSITORY", "")
    try:
        out = gh(
            "api",
            f"repos/{repo}/issues/{number}/comments",
            "--jq",
            '.[] | "---\\n@" + .user.login + " :\\n" + (.body // "")',
        )
        return out or ""
    except Exception as e:  # noqa: BLE001
        log(f"拉取评论失败（忽略）: {e}")
        return ""


def get_open_prs() -> str:
    """拉取 Open PR 标题，供 LLM 判断是否已有 PR 在修复。"""
    repo = os.environ.get("GITHUB_REPOSITORY", "")
    try:
        out = gh(
            "api",
            f"repos/{repo}/pulls",
            "--jq",
            '.[] | "#" + (.number|tostring) + " " + .title',
        )
        return out or ""
    except Exception as e:  # noqa: BLE001
        log(f"拉取 PR 列表失败（忽略）: {e}")
        return ""


def build_user_message(event: dict) -> str:
    issue = event.get("issue", {})
    number = issue.get("number", 0)
    title = issue.get("title", "")
    body = issue.get("body", "") or ""
    author = issue.get("user", {}).get("login", "")
    state = issue.get("state", "")
    labels = [lb.get("name", "") for lb in issue.get("labels", [])]

    # 评论事件：把触发评论也拼进正文上下文
    if event.get("comment"):
        comment_body = event.get("comment", {}).get("body", "") or ""
        comment_author = event.get("comment", {}).get("user", {}).get("login", "")
        body += f"\n\n[新评论 by @{comment_author}]\n{comment_body}"

    comments = get_issue_comments(number)
    prs = get_open_prs()

    parts = [
        f"Issue 编号: #{number}",
        f"标题: {title}",
        f"作者: @{author}",
        f"当前状态: {state}",
        f"当前标签: {', '.join(labels) if labels else '(无)'}",
        f"正文:\n{body[:6000]}",
    ]
    if comments:
        parts.append(f"已有评论:\n{comments[:4000]}")
    if prs:
        parts.append(f"当前 Open PR:\n{prs[:2000]}")

    return "\n\n".join(parts)


# ---------------------------------------------------------------------------
# 3. 调用自定义 LLM API
# ---------------------------------------------------------------------------

def call_llm(system_prompt: str, user_message: str) -> str:
    base = read_env("CUSTOM_API_BASE_URL").rstrip("/")
    key = read_env("CUSTOM_API_KEY")
    model = read_env("CUSTOM_API_MODEL", "deepseek-chat")

    url = f"{base}/chat/completions"
    payload = {
        "model": model,
        "messages": [
            {"role": "system", "content": system_prompt},
            {"role": "user", "content": user_message},
        ],
        "temperature": 0.2,
    }
    req = urllib.request.Request(
        url,
        data=json.dumps(payload).encode("utf-8"),
        headers={
            "Content-Type": "application/json",
            "Authorization": f"Bearer {key}",
        },
        method="POST",
    )
    log(f"调用 LLM API: {url} (model={model})")
    try:
        with urllib.request.urlopen(req, timeout=120) as resp:
            data = json.loads(resp.read().decode("utf-8"))
    except urllib.error.HTTPError as e:
        log(f"API 调用失败: {e.code} {e.read().decode('utf-8', 'ignore')}")
        raise
    return data["choices"][0]["message"]["content"]


def extract_json(text: str) -> dict:
    """从模型输出中鲁棒地提取 JSON（兼容被 Markdown 代码块包裹的情况）。"""
    text = text.strip()
    # 去掉 ```json ... ``` 或 ``` ... ``` 包裹
    if text.startswith("```"):
        text = text.strip("`")
        # 去掉可能的语言标识首行
        first_nl = text.find("\n")
        if first_nl != -1:
            head = text[:first_nl].strip().lower()
            if head in ("json", "javascript", "js"):
                text = text[first_nl + 1:]
    start = text.find("{")
    end = text.rfind("}")
    if start == -1 or end == -1 or end <= start:
        raise RuntimeError(f"无法从模型输出中解析 JSON: {text[:500]}")
    return json.loads(text[start:end + 1])


# ---------------------------------------------------------------------------
# 4. 执行决策
# ---------------------------------------------------------------------------

def apply_actions(d: dict, issue: dict) -> None:
    number = issue.get("number", 0)
    title = issue.get("title", "")
    author = issue.get("user", {}).get("login", "")

    # 4.1 标题前缀
    prefix = d.get("title_prefix")
    if prefix and not title.startswith(prefix):
        new_title = f"{prefix} {title}"
        gh("issue", "edit", str(number), "--title", new_title, check=False)
        log(f"标题已加前缀 -> {new_title}")

    # 4.2 标签
    labels = d.get("labels") or []
    if labels:
        gh("issue", "edit", str(number), "--add-label", ",".join(labels), check=False)
        log(f"已添加标签: {labels}")

    # 4.3 评论
    comment = (d.get("comment") or "").strip()
    analysis = (d.get("analysis") or "").strip()
    if analysis and analysis not in comment:
        comment = f"{comment}\n\n---\n**分析结论**：{analysis}".strip()
    if comment:
        gh("issue", "comment", str(number), "--body", comment, check=False)
        log("已发布评论")

    # 4.4 关联重复 Issue / PR（通过评论引用）
    dup = d.get("duplicate_of")
    if dup:
        note = f"关联重复 Issue：# {dup}"
        gh("issue", "comment", str(number), "--body", note, check=False)
        log(note)
    pr = d.get("link_pr")
    if pr:
        note = f"已关联在修复中的 PR：# {pr}"
        gh("issue", "comment", str(number), "--body", note, check=False)
        log(note)

    # 4.5 关闭
    if d.get("close"):
        gh("issue", "close", str(number), check=False)
        log("已关闭 Issue")

    # 4.6 锁定
    if d.get("lock"):
        gh("issue", "lock", str(number), check=False)
        log("已锁定 Issue")

    # 4.7 封禁用户（GITHUB_TOKEN 通常无 /user/blocks 权限，尽力而为）
    if d.get("ban") and author:
        try:
            gh("api", "-X", "PUT", f"user/blocks/{author}", check=False)
            log(f"已尝试封禁用户 @{author}")
        except Exception as e:  # noqa: BLE001
            log(f"封禁失败（需具有 admin 权限的 PAT）: {e}")


# ---------------------------------------------------------------------------
# 主流程
# ---------------------------------------------------------------------------

def main() -> int:
    try:
        event = load_event()
        issue = event.get("issue", {})
        if not issue:
            log("事件中无 issue 数据，跳过")
            return 0

        number = issue.get("number", 0)
        log(f"开始处理 Issue #{number}")

        system_prompt = load_system_prompt(read_env("SYSTEM_PROMPT_FILE"))
        user_message = build_user_message(event)

        raw = call_llm(system_prompt, user_message)
        log(f"模型原始输出:\n{raw[:1000]}")

        decision = extract_json(raw)
        log(f"解析后的决策:\n{json.dumps(decision, ensure_ascii=False, indent=2)}")

        action = decision.get("action", "none")
        log(f"决策动作: {action}")
        if action in ("none", ""):
            log("无需执行操作")
            return 0

        apply_actions(decision, issue)
        return 0
    except Exception as e:  # noqa: BLE001
        log(f"处理失败: {e}")
        return 1


if __name__ == "__main__":
    sys.exit(main())
