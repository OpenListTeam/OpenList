# Phase 0 — Non-Destructive First-Run Audit
**Project:** Open-Box (`open-box`)  
**Date:** 2026-08-20  
**Auditor role:** Principal Platform / DevOps / Security / DB / Release

---

## Inventory summary

| Area | Status |
|------|--------|
| Production app | **EXISTING** — online |
| Supabase Postgres | **EXISTING** — healthy, OpenList tables present |
| Supabase Auth | **PARTIAL** — project exists; app uses OpenList JWT, not Supabase Auth |
| Railway volume | **EXISTING** — protected |
| Fork repo | **EXISTING** — near-upstream OpenList |
| Railway-safe Dockerfile | **BROKEN** — `VOLUME` directive |
| Branch strategy (staging/develop) | **MISSING** |
| Upstream auto-sync to isolated branch | **PARTIAL** — upstream has `sync_repo.yml`; not Open-Box gated |
| Docker Hub versioned Open-Box images | **MISSING** |
| Staging env | **MISSING** |
| Verified backups (DB + volume) | **MISSING** |
| AI PR gate | **MISSING** (ops kit prepared locally) |
| Jobs/cron/queue ops layer | **MISSING** (OpenList internal tasks EXISTING) |
| RLS on OpenList tables | **OUTDATED/N/A** — service-role app access; not multi-tenant Supabase Auth |
| Secrets in chat history | **SECURITY RISK** |

---

## 1. Source control — `hillstreet-ph/open-box`

| Item | Finding | Class |
|------|---------|-------|
| Structure | Full OpenList tree: `cmd`, `drivers`, `internal`, `server`, `public`, `build.sh` | EXISTING |
| Upstream relationship | Content is OpenList fork; remote `upstream` not verified from this agent | PARTIAL |
| Branches | Default `main` only observed | PARTIAL |
| Tags/releases | Upstream-style CI present; Open-Box release channel not established | PARTIAL |
| Dockerfile | Contains `VOLUME /opt/openlist/data/` | **BROKEN** for Railway |
| entrypoint.sh | Permission checks for `./data` | EXISTING |
| docker-compose.yml | Local only, official image path | EXISTING |
| GitHub Actions | beta_release, build, release, release_docker, sync_repo, test_docker, renovate | EXISTING (upstream-oriented) |
| Custom Open-Box overlays | Minimal / none beyond rename intent | PARTIAL |
| AGPL LICENSE | Present | EXISTING |
| Renovate | Present | EXISTING |

**UPSTREAM-CONFLICT RISK:** High if Dockerfile and branding patches are not isolated; prefer overlay patches.

**RECOMMENDED:** Remove `VOLUME`; keep upstream structure; put ops workflows in separate `open-box-ops` or additive workflow files.

---

## 2. Railway production

| Item | Value | Class |
|------|-------|-------|
| Project | `open-box` (`d6596499-…`) | EXISTING |
| Service | `openlist-railway` | EXISTING |
| URL | https://openlist-railway-production-2100.up.railway.app | EXISTING |
| Image | `openlistteam/openlist:latest` | EXISTING (not yet own fork image) |
| Volume | `openlist-railway-volume` → `/opt/openlist/data` (~1MB/500MB) | EXISTING — **PROTECTED** |
| Region | Southeast Asia | EXISTING |
| Health | Online; `/ping` → `pong` | EXISTING |
| Unused DB service | Railway `Postgres` | DUPLICATED vs Supabase — safe to leave offline or delete later |
| Env | Effective: `DB_*`, `SITE_URL`, `PORT`, `TZ` | EXISTING (cleaned duplicates this session) |

**DATA-LOSS RISK:** Deleting volume or pointing `DB_*` at empty DB.  
**RECOMMENDED:** Pin image digest; create `staging` environment with separate volume.

---

## 3. Supabase — project `open-box` (`ymhiwerqyegvondndkjn`)

| Item | Finding | Class |
|------|---------|-------|
| Status | ACTIVE_HEALTHY, region `ap-northeast-1` | EXISTING |
| Postgres | 17.x | EXISTING |
| OpenList tables | `x_users`, `x_storages`, `x_meta`, … | EXISTING |
| Auth site_url | Updated to production Railway URL this session | EXISTING (hardened) |
| Email auth | Enabled | EXISTING |
| GitHub OAuth | Disabled | PARTIAL |
| Storage buckets | Not fully audited via API this pass | PARTIAL |
| pgvector | Not verified enabled | MISSING / UNKNOWN |
| Other projects in org | open-command, open-hide, open-system, … | EXISTING (separate) |

**Auth architecture note:** Production login is **OpenList JWT + `x_users`**, not Supabase Auth sessions. Forcing Supabase Auth into OpenList core without a gateway is **DATA-LOSS / BREAKING** risk.

---

## 4. Authentication

| Layer | Status |
|-------|--------|
| OpenList admin/`x_users` | EXISTING — production |
| Supabase Auth | PARTIAL — configured at project level; not driving OpenList UI |
| Service role in browser | Must never happen — SECURITY RISK if misused |
| OAuth callbacks | Not wired to OpenList | MISSING |

---

## 5. Storage / S3 / vectors / jobs

| Capability | Status |
|------------|--------|
| OpenList multi-storage drivers | EXISTING (in code) |
| S3-compatible config via env | PARTIAL — not fully wired in Railway vars |
| Railway volume files | EXISTING — small usage |
| Vector/pgvector pipeline | MISSING |
| Ops cron (backup/sync) | MISSING |
| OpenList internal tasks | EXISTING |
| Durable external queue | MISSING (may not be required day-1) |

---

## 6. Docker Hub

| Item | Status |
|------|--------|
| Account `huxleysee` | Credentials previously provided | PARTIAL |
| Image `huxleysee/open-box` | Not verified published | MISSING |
| Multi-arch + digests | MISSING |

---

## 7. Security

| Issue | Class |
|-------|-------|
| Secrets pasted in chat (DB password, service role, Railway token, Docker PAT, sbp_) | **SECURITY RISK** — rotate after platform stable |
| `OPENLIST_ADMIN_PASSWORD` left in Railway | Removed this session | MITIGATED |
| Duplicate DB env prefixes | Cleaned this session | MITIGATED |
| Dockerfile VOLUME | **BROKEN** | blocks fork deploy |
| GitHub Actions write from this agent | Blocked (403) | BLOCKED |

---

## 8. Recommended phase order (unchanged from master prompt)

0. Audit — **this document**  
1. Repo safety — Dockerfile fix, branches, protection (needs GitHub write)  
2. CI gates on fork  
3. Upstream sync → isolated branch → PR  
4. Docker Hub versioning  
5. Staging  
6. Supabase expansions (storage buckets, optional Auth gateway later)  
7. Railway pin to own image digest  
8. Backups + verification  
9. Jobs/cron  
10. Controlled production promotion  

---

## 9. Explicit non-actions (this session)

- No DROP/TRUNCATE/volume delete  
- No database reset  
- No forced Supabase Auth replacement of OpenList login  
- No force-push  
- No from-scratch rebuild  
