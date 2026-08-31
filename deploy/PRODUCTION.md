# Open-Box Production

Canonical domain: `https://open-box.space`

## Topology

`open-box.space -> Cloudflare -> Railway Open-Box/OpenList -> Master Storage`

Source collection remains independent:

`Google Drive / Dropbox / OneDrive -> read+copy collector -> Master Storage`

Source files are never automatically moved or deleted. Source deletion must not propagate to the master repository.

## Responsibilities

- GitHub: source, CI/CD and reviewed production changes.
- Cloudflare: DNS/TLS/front-door gateway for `open-box.space`.
- Railway: persistent Open-Box/OpenList application runtime.
- Supabase: PostgreSQL/application data and optional gateway authentication.
- Master Storage: canonical collected file repository, independent from the OpenList application server.
- rclone worker: one-way collection from external storage into Master Storage.

## Required runtime variables

Open-Box application:

- `PORT=5244`
- `SITE_URL=https://open-box.space`
- `TZ=Asia/Manila`
- `UMASK=022`
- `DB_TYPE=postgres`
- `DB_HOST`
- `DB_PORT=5432`
- `DB_USER`
- `DB_PASSWORD` (secret)
- `DB_NAME=postgres`
- `DB_SSL_MODE=require`

Cloudflare gateway:

- `APP_NAME=Open-Box`
- `ORIGIN_URL=<Railway private/public application origin>`
- `SUPABASE_URL`
- `SUPABASE_PUBLISHABLE_KEY` (secret)
- `AUTH_REQUIRED=false` until login flow is verified end-to-end

Storage collector:

- `MASTER_REMOTE=master`
- `MASTER_ROOT=open-box`
- populated `rclone.conf` mounted from runtime secrets/persistent storage; never committed

## Persistence

`/opt/openlist/data` must remain on a persistent Railway volume. Master Storage must be separate from this volume so loss/replacement of the application runtime does not destroy collected files.

## DNS

Production apex `open-box.space` should route through Cloudflare to the Open-Box gateway/origin. Do not expose the Master Storage backend publicly unless a specific storage protocol requires it.

## Deployment gate

Before production promotion verify: application health, persistent volume, PostgreSQL connectivity, source mounts, non-destructive copy behavior, Master Storage write/read, OpenList Master Storage mount, domain/TLS, restart recovery, and restore procedure.
