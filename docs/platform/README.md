# Open-Box Platform

Open-Box is HillStreet's branded distribution of the OpenList engine, deployed
on Railway or Zeabur with Supabase as its PostgreSQL source of truth and an
optional Cloudflare OAuth gateway.

## Branches

- `main` — production-ready
- `staging` — release candidates
- `develop` — active development
- `upstream-sync/*` — automated upstream integration (PR to staging only)

## Production

- Railway project `open-box`, service `openlist-railway`
- Database: Supabase Postgres (session pooler)
- Auth: OpenList JWT (`x_users`); Supabase Auth reserved for future gateway

## Hosting roles

- Railway or Zeabur runs the stateful Go service and mounts `/opt/openlist/data`.
- Supabase Postgres stores the Open-Box application tables.
- Cloudflare provides DNS, TLS, WAF, reverse proxying, and optional GitHub OAuth.
- Supabase Auth and Open-Box JWT are deliberately separate trust layers.

## Railway requirement

Dockerfile must **not** contain `VOLUME` — Railway mounts `/opt/openlist/data` externally.
