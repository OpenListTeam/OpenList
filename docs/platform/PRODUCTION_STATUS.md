# Open-Box production status

**Live URL:** https://openlist-railway-production-2100.up.railway.app

## Ownership map

| System | Owner account | Resource |
|--------|---------------|----------|
| GitHub | hillstreet-ph / master-kanor | `hillstreet-ph/open-box` |
| Docker Hub | openclose8 | `openclose8/open-box` |
| Railway | Kobeplay Workspace | project `open-box`, service `openlist-railway` |
| Supabase | HillStreet | project `open-box` (`ymhiwerqyegvondndkjn`) |

## Data plane (do not confuse)

| Component | Use? |
|-----------|------|
| Supabase Postgres | **YES** — app SoT (`x_users`, `x_storages`, …) |
| Railway volume `/opt/openlist/data` | **YES** — config/cache |
| Railway Postgres plugin | **NO** — empty leftover; delete in UI to save cost |
| Supabase Storage `open-box-backups` | backups |
| Supabase Storage `open-box-files` | optional S3 driver target |
| pgvector extension | enabled for future use |

## Auth

- App login: OpenList JWT + `x_users` in Supabase
- Supabase Auth: available for future apps/OAuth providers (configure Client IDs in dashboard)

## Automation

- `openbox-upstream-sync.yml` → branch + draft PR to `staging`
- `openbox-pr-gate.yml` → blocks Dockerfile VOLUME
- `openbox-docker.yml` → Docker Hub tags
- `openbox-backup.yml` → daily `pg_dump`

## Security note (Data API)

OpenList uses **direct Postgres** (`DB_*`), not the Supabase Data API, for runtime.
Tables may still appear in Data API exposure settings. Prefer **not** granting anon broad access to `x_*` tables; use service role only server-side.
