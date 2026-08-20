# Production test report

**URL:** https://openlist-railway-production-2100.up.railway.app  
**Date:** 2026-08-20

| Feature | Result |
|---------|--------|
| `/ping` | PASS |
| Public settings / site_title Open-Box | PASS |
| Admin login JWT | PASS |
| `/api/me` admin role | PASS |
| User list admin+guest | PASS |
| Storage `/supabase-s3` S3 work | PASS |
| FS list root | PASS |
| FS upload to S3 | PASS |
| FS list after upload | PASS |
| Offline download tasks API | PASS |
| Copy tasks API | PASS |
| Drivers catalog | PASS |
| Frontend title Open-Box | PASS |
| Supabase tables x_* | PASS |
| Supabase storage buckets | PASS |
| GitHub OAuth (Supabase Auth) | ENABLED |
| OpenList UI GitHub SSO | NOT wired (JWT login is primary) |
| Local storage mount | Not configured (S3 only) |
| Vector search product UI | Extension only (no OpenList UI) |
