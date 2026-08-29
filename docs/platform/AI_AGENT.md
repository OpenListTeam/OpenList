# Open-Box AI agent credentials

Stored as GitHub Actions secrets and Railway env (server-side only):

| Secret | Purpose |
|--------|---------|
| `NVIDIA_API_KEY` | NVIDIA NIM / integrate.api.nvidia.com |
| `NVIDIA_API_KEY_PERSONAL` | Secondary NVIDIA key |
| `OPENROUTER_API_KEY` | OpenRouter multi-model gateway |

## Models

Preferred via OpenRouter: `nvidia/llama-3.1-nemotron-70b-instruct`  
Direct NVIDIA: `meta/llama-3.1-8b-instruct` (and catalog at build.nvidia.com)

## Workflow

`.github/workflows/openbox-ai-review.yml` runs on PRs and manual dispatch.

Never commit API keys. Rotate if exposed in chat.
