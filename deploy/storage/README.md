# Open-Box Master Storage Ingestion

## Policy

Open-Box uses a non-destructive, one-way collection model:

`Google Drive / Dropbox / OneDrive -> rclone copy -> Master Storage -> OpenList`

Original files remain in their source providers. Source deletion never automatically deletes the collected master copy. Master deletion never modifies a source account.

## Sources

- Google Drive: Personal-01, Personal-02, Workspace-01, Shared-Drive-01
- Dropbox: Account-01, Account-02
- OneDrive: Personal-01, Business-01, Business-02

## Security

Never commit OAuth refresh tokens, client secrets, passwords, S3 keys, or a populated `rclone.conf`. The repository contains only a template. Mount the real configuration at `/config/rclone/rclone.conf` from a secret/persistent runtime location.

Where possible, source credentials should be read-only. Master credentials require write access only to the designated Open-Box prefix/bucket.

## Master layout

```
open-box/
  sources/
    google-personal-01/
    google-personal-02/
    google-workspace-01/
    google-shared-drive-01/
    dropbox-account-01/
    dropbox-account-02/
    onedrive-personal-01/
    onedrive-business-01/
    onedrive-business-02/
```

This first implementation preserves provenance by source. A catalog/index layer can subsequently expose a logical Unified Library and content-hash deduplication without deleting source originals.

## Collection

Run `deploy/storage/collect.sh` in an rclone container/worker with:

- `/config/rclone/rclone.conf` mounted read-only
- `/logs` persistent
- environment `MASTER_REMOTE=master`
- environment `MASTER_ROOT=open-box`

The script deliberately uses `rclone copy`, not `rclone sync`.

## Deduplication design

Do not deduplicate by filename. The inventory layer should track provider, account, source object/path, size, modified time, content hash when available/calculated, master object key, verification status, and timestamps. Identical content may reference one canonical master object while retaining every source relationship.

## Recovery

OpenList application state must remain on its persistent `/opt/openlist/data` volume. Master Storage must be independent of the OpenList container/server. If OpenList is replaced, restore its persistent state and remount Master Storage; source and master files remain independently stored.
