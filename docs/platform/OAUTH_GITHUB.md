# GitHub OAuth (Supabase Auth)

Configured for project `ymhiwerqyegvondndkjn`.

| Setting | Value |
|---------|--------|
| Client ID | `Iv23litJbuZ6Afp9mVU2` |
| Callback URL (set in GitHub OAuth App) | `https://ymhiwerqyegvondndkjn.supabase.co/auth/v1/callback` |
| App site URL | `https://openlist-railway-production-2100.up.railway.app` |

## Important

- **Supabase Auth GitHub login** is enabled at the project level.
- **OpenList / Open-Box admin UI login** still uses OpenList JWT (`x_users`) by default.
- To use GitHub OAuth *inside* the Open-Box file UI, configure OpenList SSO / OIDC against Supabase, or build a small auth gateway. Enabling the provider alone does not replace the OpenList login form.

## Verify

1. GitHub OAuth App → Authorization callback URL must match Supabase callback above.
2. Supabase Dashboard → Authentication → Providers → GitHub = enabled.
3. Test: `https://ymhiwerqyegvondndkjn.supabase.co/auth/v1/authorize?provider=github`
