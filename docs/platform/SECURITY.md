# Security — Open-Box

## Trust boundaries

| Component | Trust |
|-----------|--------|
| Browser | Public; never service_role |
| OpenList process | Server; holds DB password |
| Supabase PostgREST | RLS optional; OpenList uses direct Postgres |
| GitHub Actions | CI secrets only |
| Railway | Production secrets |

## Secrets classification

| Name pattern | Class |
|--------------|-------|
| `SITE_URL`, `PORT` | PUBLIC / SERVER |
| `DB_*` | SERVER_SECRET / PRODUCTION |
| Supabase `service_role` / `sb_secret_*` | SERVER_SECRET / CI_ONLY |
| Docker Hub PAT | CI_ONLY |
| Railway token | CI_ONLY / PRODUCTION_ONLY |
| OpenList JWT secret (in volume config) | SERVER_SECRET |

## Auth policy (current production)

- Application authentication = OpenList JWT + `x_users`  
- Supabase Auth is project-ready for future gateway/UI; not the live OpenList login path  
- Do not expose service_role to clients  

## Rotation triggers

- Any secret pasted into chat, tickets, or public logs  
- Staff offboarding  
- Suspected compromise  

## Container

- Prefer non-root when volume permissions allow  
- No Docker `VOLUME` instruction on Railway  
- Pin image digests in production  
