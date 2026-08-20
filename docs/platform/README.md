# Open-Box Platform

Production distribution of OpenList on Railway + Supabase.

## Branches

- `main` — production-ready
- `staging` — release candidates
- `develop` — active development
- `upstream-sync/*` — automated upstream integration (PR to staging only)

## Production

- Railway project `open-box`, service `openlist-railway`
- Database: Supabase Postgres (session pooler)
- Auth: OpenList JWT (`x_users`); Supabase Auth reserved for future gateway

## Railway requirement

Dockerfile must **not** contain `VOLUME` — Railway mounts `/opt/openlist/data` externally.
