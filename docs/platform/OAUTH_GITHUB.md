# GitHub OAuth (Supabase Auth)

| Setting | Value |
|---------|--------|
| App ID | 4658661 |
| Client ID | `Iv23lipQrjINz73M68vL` |
| Callback URL | `https://ymhiwerqyegvondndkjn.supabase.co/auth/v1/callback` |
| App site URL | `https://openlist-railway-production-2100.up.railway.app` |

Supabase Auth GitHub provider is enabled with the current client credentials.
Open-Box file UI login remains OpenList JWT (`x_users`). The optional Cloudflare
gateway in `deploy/cloudflare` adds a separate GitHub-authenticated front door; it
does not silently create, merge, or replace Open-Box accounts.

## Cloudflare gateway rollout

1. Add the exact production URL `https://<open-box-domain>/auth/callback` to the
   Supabase Auth redirect allow list.
2. From `deploy/cloudflare`, install dependencies and run `npm test`.
3. Set `ORIGIN_URL` in `wrangler.toml` to the active Railway or Zeabur origin.
4. Run `wrangler secret put SUPABASE_PUBLISHABLE_KEY`.
5. Deploy the Worker and bind the production custom domain.
6. Test `/auth/login`, `/auth/me`, upload, download, WebDAV, and range requests.
7. Change `AUTH_REQUIRED` to `true` only after the OAuth test passes. Keeping it
   `false` during rollout prevents an authentication lockout.

The GitHub OAuth App callback remains the Supabase callback URL shown above. The
application callback is configured in Supabase's redirect allow list, not in the
GitHub OAuth App.
