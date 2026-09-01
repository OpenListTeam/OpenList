# Disaster Recovery — Open-Box

## Goal

Reconstruct production without “starting from scratch” using artifacts you control.

## Minimum recovery set

1. **Git** — `hillstreet-ph/open-box` at known tag/SHA  
2. **Image** — preferred `openclose8/open-box@sha256:…` (until then `openlistteam/openlist` digests)  
3. **Database** — Supabase logical backup / PITR of project `ymhiwerqyegvondndkjn`  
4. **Volume** — archive of `/opt/openlist/data` (config, local files, indexes)  
5. **Env** — Railway variable names from `config/railway.env.example` (values from secret store)  
6. **DNS** — public hostname → Railway service  

## Recovery procedure (new Railway project)

1. Create Railway project `open-box`.  
2. Create service from image digest (not only `latest`).  
3. Attach volume at `/opt/openlist/data`; restore volume archive before first traffic.  
4. Set `DB_*` to Supabase session pooler; restore DB if empty.  
5. Set `SITE_URL` to final public URL.  
6. Deploy; run smoke: `/ping`, login, `/api/me`.  
7. Re-point DNS.

## What is NOT required

- Re-running OpenList first-boot password generation if `x_users` restored  
- Recreating storages if `x_storages` restored  

## RPO / RTO targets (define operationally)

| Metric | Target (set with team) |
|--------|------------------------|
| RPO | ≤ 24h until daily backups exist |
| RTO | ≤ 4h once backups verified |

## Forbidden recovery actions

- Creating a new empty Supabase project and abandoning old data  
- Deleting the only volume backup to “make space”  
