# Open-Box data plane (source of truth)

## Do not use Railway Postgres for the app

The Railway service named **Postgres** (if present) was a temporary/duplicate service.
It shows **“You have no tables”** because the application never wrote there.

| Store | Role |
|-------|------|
| **Supabase Postgres** `ymhiwerqyegvondndkjn` | **Source of truth** — OpenList tables (`x_users`, `x_storages`, …) |
| **Railway volume** `/opt/openlist/data` | Config, local cache, indexes |
| **Supabase Storage** `open-box-backups` | Logical DB backups |
| **Supabase Storage** `open-box-files` | Optional S3-compatible file backend |

## Connection used by `openlist-railway`

OpenList env prefix: `DB_`

- `DB_TYPE=postgres`
- `DB_HOST=aws-0-ap-northeast-1.pooler.supabase.com` (session pooler, IPv4)
- `DB_PORT=5432`
- `DB_USER=postgres.ymhiwerqyegvondndkjn`
- `DB_NAME=postgres`
- `DB_SSL_MODE=require`

Direct host form (often IPv6-only; prefer pooler on Railway):

`postgresql://postgres:[PASSWORD]@db.ymhiwerqyegvondndkjn.supabase.co:5432/postgres`

## Authentication

| Layer | Implementation |
|-------|----------------|
| **Production app login** | OpenList JWT + rows in Supabase table `x_users` |
| **Supabase Auth** | Project configured (`site_url` = Railway URL). Email enabled. GitHub/Google OAuth need provider apps before enable. |
| **OAuth into OpenList UI** | Requires OpenList SSO settings **or** a separate gateway; not automatic when toggling Supabase providers. |

Do not switch Railway `DB_*` to the empty Railway Postgres service — that would look like a “fresh install” and orphan existing users.
