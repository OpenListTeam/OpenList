#!/usr/bin/env python3
import json, os, urllib.request

ctx = open("/tmp/ctx.txt").read()[:12000]
prompt = os.environ.get("USER_PROMPT") or (
    "Review this Open-Box change set for production risk "
    "(Railway, Supabase, Dockerfile VOLUME, data loss). Be concise."
)
messages = [
    {
        "role": "system",
        "content": (
            "You are Open-Box platform reviewer. Flag blockers: "
            "VOLUME in Dockerfile, DB drops, secret leaks, auth regressions."
        ),
    },
    {"role": "user", "content": prompt + "\n\nContext:\n" + ctx},
]

or_key = os.environ.get("OPENROUTER_API_KEY") or ""
nv_key = os.environ.get("NVIDIA_API_KEY") or ""

if or_key:
    url = "https://openrouter.ai/api/v1/chat/completions"
    model = "meta-llama/llama-3.1-8b-instruct"
    auth = or_key
elif nv_key:
    url = "https://integrate.api.nvidia.com/v1/chat/completions"
    model = "meta/llama-3.1-8b-instruct"
    auth = nv_key
else:
    open("/tmp/ai-review.md", "w").write("No AI API keys configured.")
    raise SystemExit(0)

payload = {
    "model": model,
    "messages": messages,
    "max_tokens": 600,
    "temperature": 0.2,
}
req = urllib.request.Request(
    url,
    data=json.dumps(payload).encode(),
    headers={"Authorization": "Bearer " + auth, "Content-Type": "application/json"},
)
try:
    with urllib.request.urlopen(req, timeout=90) as r:
        d = json.load(r)
    text = d["choices"][0]["message"]["content"]
except Exception as e:
    text = "AI review unavailable: " + str(e)

open("/tmp/ai-review.md", "w").write(text)
print(text[:2000])
