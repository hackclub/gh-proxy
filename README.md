# gh-proxy

A lightweight Go app backed by Postgres that exposes a cached proxy to GitHub APIs, with donated token rotation and an admin UI.

## Development

Prereqs: Docker + Docker Compose.

1. Copy `.env.example` to `.env` and fill in `GITHUB_OAUTH_CLIENT_ID` and `GITHUB_OAUTH_CLIENT_SECRET`.
2. In one terminal, start dev stack: `docker compose up --build`
3. In another terminal, tail logs: `docker compose logs -f app`

The dev server hot-reloads via `air`. Postgres runs on port 5433 locally.

Admin basic auth creds are pulled from env: `ADMIN_USER` / `ADMIN_PASS`.

## Deployment

Build with the provided `Dockerfile`. Deploy on Coolify as a single container with `DATABASE_URL`, `ADMIN_*`, `MAX_CACHE_*` and GitHub OAuth env vars.

## API Usage

Send requests through the proxy with your admin-created API key:

```
curl -H "X-API-Key: YOUR_KEY" "http://localhost:8080/gh/repos/zachlatta/sshtron"
```

GraphQL:

```
curl -H "X-API-Key: YOUR_KEY" -H "Content-Type: application/json" -d '{"query":"{ viewer { login } }"}' http://localhost:8080/gh/graphql
```

Note: `/gh/...` is the REST proxy prefix and `/gh/graphql` proxies GraphQL.
