# Railway Deployment Guide

Deploy OpenList on Railway with persistent storage and automatic HTTPS.

## Prerequisites

- A [Railway](https://railway.app) account
- This repository connected to Railway (or a new project from this repo)

## Quick Start

### 1. Create a New Project

1. Go to [Railway Dashboard](https://railway.app/dashboard)
2. Click **"New Project"**
3. Select **"Deploy from GitHub repo"** (or upload this repo)
4. Choose this repository

### 2. Railway will Auto-Detect

Railway will automatically detect the `railway.json` or `railway.toml` in the repo root and use `Dockerfile.railway` for building.

### 3. Add a Volume (Persistent Storage)

OpenList stores data (database, uploads, configs) in `/opt/openlist/data`. You need a persistent volume:

1. In your Railway project, click **"New"** → **"Volume"**
2. Name it `openlist-data`
3. Mount it to `/opt/openlist/data` in your service

### 4. Configure Environment Variables

Go to your service → **Variables** tab and add:

| Variable | Value | Description |
|----------|-------|-------------|
| `UMASK` | `022` | File permissions mask |
| `TZ` | `Asia/Shanghai` | Timezone (optional) |
| `RUN_ARIA2` | `false` | Enable aria2 (optional, increases build time) |
| `INSTALL_FFMPEG` | `false` | Install ffmpeg (optional, increases build time) |
| `INSTALL_ARIA2` | `false` | Install aria2 (optional, increases build time) |

> **Note:** Railway's dynamic `PORT` is automatically mapped to `HTTP_PORT` by the entrypoint script. You do **not** need to manually set `HTTP_PORT=$PORT`.

### 5. Deploy

1. Railway will automatically build and deploy
2. Wait for the build to complete (first build takes ~3-5 minutes)
3. Check the **Deployments** tab for build logs

### 6. Get Admin Credentials

On first startup, OpenList generates a random admin password. Find it in the deployment logs:

1. Go to **Deployments** → click on the latest deployment
2. View **Logs**
3. Look for: `Successfully created the admin user and the initial password is: XXXXXXXX`

Alternatively, you can set a fixed admin password by running:
```
openlist admin set YOUR_PASSWORD
```

### 7. Access OpenList

Once deployed, Railway will provide a public URL like:
```
https://your-project.up.railway.app
```

Open this URL in your browser and log in with the admin credentials.

## Configuration

### Database

By default, OpenList uses SQLite and stores the database in the volume at `/opt/openlist/data/data.db`.

For better performance, you can use Railway's PostgreSQL:

1. Add a **PostgreSQL** database to your Railway project
2. Set these environment variables:
   - `DB_TYPE=postgresql`
   - `DB_HOST=${{Postgres.HOSTNAME}}`
   - `DB_PORT=${{Postgres.PORT}}`
   - `DB_USER=${{Postgres.USERNAME}}`
   - `DB_PASS=${{Postgres.PASSWORD}}`
   - `DB_NAME=${{Postgres.DATABASE}}`

### Custom Domain

1. Go to **Settings** → **Domains**
2. Add your custom domain
3. Railway will automatically provision an SSL certificate

### Build Options

Edit `railway.json` or `railway.toml` to customize:

- `restartPolicyType`: `"on-failure"` or `"always"`
- `healthcheckPath`: Health check endpoint
- `INSTALL_FFMPEG`: Set to `true` to enable ffmpeg
- `INSTALL_ARIA2`: Set to `true` to enable aria2

## Troubleshooting

### Build Fails

- Check build logs for missing dependencies
- Ensure `go.mod` and `go.sum` are present
- Try rebuilding with `INSTALL_FFMPEG=false` and `INSTALL_ARIA2=false`

### Port Not Accessible

- Ensure `HTTP_PORT=$PORT` is set
- Check that the service is listening on `0.0.0.0` (default)

### Data Lost After Redeploy

- Ensure the volume is mounted to `/opt/openlist/data`
- Check volume status in Railway dashboard

### Cannot Login

- Check deployment logs for admin password
- Ensure database file exists in volume

## Manual Railway CLI Deployment

If you prefer using the Railway CLI:

```bash
# Install Railway CLI
npm i -g @railway/cli

# Login
railway login

# Initialize project
railway init

# Link to existing project
railway link

# Deploy
railway up
```

## Architecture

```
Railway Project
├── Service: OpenList
│   ├── Build: Dockerfile.railway
│   ├── Port: $PORT (mapped to HTTP_PORT)
│   └── Volume: /opt/openlist/data
└── (Optional) PostgreSQL Database
```

## Files Added

- `railway.json` - Railway project configuration
- `railway.toml` - Alternative Railway configuration
- `Dockerfile.railway` - Self-contained Dockerfile for Railway
- `.railwayignore` - Files excluded from Railway builds
