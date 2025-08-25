# gh-proxy

A lightweight Go service backed by Postgres that proxies the GitHub REST and GraphQL APIs. It provides:

- **Pooled “donated” tokens** with automatic rotation by API category (core/search/code_search/graphql)
- **DB-backed caching** of GET/HEAD responses with TTL and size limits
- **Per‑key rate limiting**
- A simple **admin UI** to create/disable API keys and view usage

**Ports:** the server listens on **:8080**.  
**Admin UI:** `http://localhost:8080/admin` (HTTP Basic Auth, creds from env).  
**API docs:** `http://localhost:8080/docs`.

---

## Quick start (Docker Compose)

**Prereqs:** Docker + Docker Compose.

1) **Create a `.env` file** in the project root. At minimum, set your GitHub OAuth app values if you want the “donate token” flow:

```env
# --- required for token-donation login (/auth/github) ---
GITHUB_OAUTH_CLIENT_ID=your_client_id
GITHUB_OAUTH_CLIENT_SECRET=your_client_secret

# --- admin login (change in production) ---
ADMIN_USER=admin
ADMIN_PASS=admin

# --- optional tuning (see full list below) ---
MAX_CACHE_TIME=300
MAX_CACHE_SIZE_MB=100
DB_MAX_CONNS=20
MAX_PROXY_BODY_BYTES=1048576
# For local dev the compose file pins BASE_URL to http://localhost:8080
````

> Tip: Create your GitHub OAuth App with
> **Homepage URL:** `http://localhost:8080`
> **Authorization callback URL:** `http://localhost:8080/auth/github/callback`
> Scope used: `read:user` (read‑only).

2. **Start the stack** (hot‑reload dev server + Postgres):

```bash
docker compose up --build
# in another terminal, view logs:
docker compose logs -f app
```

3. **Open the app**:

* Home: `http://localhost:8080/` (donate a token with GitHub)
* Admin: `http://localhost:8080/admin` (default `admin` / `admin`)
* API Docs: `http://localhost:8080/docs`

4. **Create an API key** in **/admin**. You’ll see the key **once**—copy it now.

5. **Make a request through the proxy**:

```bash
# REST example (public repo)
curl -H "X-API-Key: YOUR_KEY" \
  "http://localhost:8080/gh/repos/zachlatta/sshtron"

# GraphQL example
curl -H "X-API-Key: YOUR_KEY" -H "Content-Type: application/json" \
  -d '{"query":"{ viewer { login } }"}' \
  http://localhost:8080/gh/graphql
```

You’ll see helpful response headers like:

* `X-Gh-Proxy-Cache: hit|miss`
* `X-Gh-Proxy-Category: core|search|code_search|graphql`
* `X-Gh-Proxy-Client: <your key identifier>`
* `X-Gh-Proxy-Donor: <github username>` (when a donated token was used)

> Postgres in dev is exposed on **localhost:5433**. The app in Docker connects to `db:5432` internally.

---

## Running without Docker (local Go)

1. Start Postgres via Compose (for convenience):

```bash
docker compose up -d db
```

2. Create `.env` (same as above). The default `DATABASE_URL` points to the dev DB on **localhost:5433**.

3. Build & run:

```bash
go build -o ./bin/server ./cmd/server
./bin/server
```

Migrations run automatically at startup.

---

## Environment configuration

`gh-proxy` reads environment variables and also loads a `.env` file from the working directory.

| Variable                     | Required                      | Default                                                                                                                                                          | What it does                                                                                                                         |
| ---------------------------- | ----------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------ |
| `DATABASE_URL`               | **Prod: yes** (Dev: optional) | Dev default: `postgres://ghproxy:ghproxy@localhost:5433/ghproxy?sslmode=disable` (Compose app uses `postgres://ghproxy:ghproxy@db:5432/ghproxy?sslmode=disable`) | Postgres connection string. Migrations run automatically.                                                                            |
| `BASE_URL`                   | Yes                           | `http://localhost:8080`                                                                                                                                          | Public base URL of this service. **Must match the external scheme+host** (used for OAuth callback + WebSocket origin checks).        |
| `ADMIN_USER`                 | Yes                           | `admin`                                                                                                                                                          | HTTP Basic username for `/admin`.                                                                                                    |
| `ADMIN_PASS`                 | Yes                           | `admin`                                                                                                                                                          | HTTP Basic password for `/admin`.                                                                                                    |
| `GITHUB_OAUTH_CLIENT_ID`     | Needed for token donation     | —                                                                                                                                                                | GitHub OAuth App client ID used by `/auth/github`.                                                                                   |
| `GITHUB_OAUTH_CLIENT_SECRET` | Needed for token donation     | —                                                                                                                                                                | GitHub OAuth App client secret.                                                                                                      |
| `MAX_CACHE_TIME`             | No                            | `300`                                                                                                                                                            | Cache TTL **in seconds** for cached responses (`0` = unlimited; stored without expiry). GET/HEAD 200s only; respects public caching. |
| `MAX_CACHE_SIZE_MB`          | No                            | `100`                                                                                                                                                            | Approximate max size (in MB) of the `cached_responses` table. Oldest rows are trimmed periodically.                                  |
| `DB_MAX_CONNS`               | No                            | `20`                                                                                                                                                             | Max connections in the Postgres pool.                                                                                                |
| `MAX_PROXY_BODY_BYTES`       | No                            | `1048576`                                                                                                                                                        | Max allowed request body to `/gh/*` in bytes (returns `413` if exceeded).                                                            |

> If `GITHUB_OAUTH_CLIENT_ID/SECRET` aren’t set, the server still runs, but token donation (the “Donate Token” button) will be disabled.

---

## Endpoints

* **Homepage**: `/` — explains the project and lets users donate a GitHub token.
* **API Docs**: `/docs` — copy‑paste examples for REST/GraphQL.
* **Admin**: `/admin` — create/disable API keys, view usage, recent activity.
* **REST proxy**: `/gh/{path}` — proxies to `https://api.github.com/{path}`
* **GraphQL proxy**: `/gh/graphql` — proxies to `https://api.github.com/graphql`

All API requests require `X-API-Key: <your key>`.

---

## How it works (one‑minute version)

* **Token rotation:** Donated tokens are stored (read‑only scope). The proxy rotates tokens and tracks category‑specific GitHub rate limits. Revoked/unauthorized tokens are marked and skipped automatically.
* **Caching:** GET/HEAD successful responses are cached in Postgres with a TTL and size cap. Periodic jobs trim old cache rows and keep only recent request logs.
* **Rate limiting:** Each API key has a per‑second limit (default **10 rps**) configured when the key is created.

---

## Production (container)

Build and run as a single container (provide your own Postgres):

```bash
# Build a minimal image
docker build -t gh-proxy:latest -f Dockerfile .

# Run (example)
docker run --rm -p 8080:8080 --env-file .env \
  -e DATABASE_URL="postgres://user:pass@host:5432/ghproxy?sslmode=disable" \
  gh-proxy:latest
```

> In production, set `BASE_URL` to your public HTTPS URL (e.g., `https://proxy.example.org`) so OAuth and the admin WebSocket work correctly. Change `ADMIN_USER/ADMIN_PASS`.

---

## Troubleshooting

* **OAuth login fails / admin live stats don’t update:** Ensure `BASE_URL` exactly matches the public origin (scheme + hostname + port).
* **“missing X-API-Key” (401):** Include your API key header on `/gh/*` requests.
* **429 Too Many Requests:** Your key hit its per‑second rate limit; lower concurrency or request fewer times per second.
* **413 Request Entity Too Large:** Increase `MAX_PROXY_BODY_BYTES` if you need to send larger GraphQL payloads.

---

## Development notes

* Hot‑reload is provided in the dev container via `air`.
* Server logs include request lines, admin actions, OAuth events, cache activity, and errors.
* See **`DEVELOPMENT.md`** for logging commands and tips.
