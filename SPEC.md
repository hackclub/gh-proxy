gh-proxy is a lightweight Go app backed by a Postgres DB that exposes a cached proxy to the GitHub REST, GraphQL, and search APIs.

It works by having a public page at / where anyone can log in with GitHub to donate an access token. That page should show:

- A brief explanation of what gh-proxy is: "gh-proxy is a small service by Hack Club that lets us make cached public API calls beyond rate limits to do things like understand how many people have shipped projects in Hack Club events, etc"
- The total # of people who have donated access tokens
- The username of the last person who donated
- An explanation of what the access token is used for and that it gives no private permissions, it only gives read-only permissions to everything on GitHub.com

On /admin is a page that is authenticated with HTTP basic auth with credentials pulled from the environment. On that page you can create API keys, disable API keys, and see usage by API keys.

When you create an API key, it should ask for the following fields:

- Hack Club username (ex. "zrl")
- App name (ex. "dev")
- Machine (ex. "shinx" or "coolify")
- Rate limit per second (default: 10)

The API keys should then be generated and look like this: zrl_dev_shinx_lkdsjlaksdjflkjweasdf.

The admin page should use websockets and show:

- Total # of requests
- Cache hit rate
- Today's request count
- # of active donated API keys

And a table of API keys and their usage. Columns on table: "Total requests", "Cache hit rate", Last used, Daily usage (7d - chart), Actions (Disable)

Below that it should show a table called "Recent Activity" and use websockets to show a realtime list of recent requests, ex. 'GET /repos/zachlatta/sshtron')

# How donated API key rotation works

GitHub keeps track of multiple rate limit types per access token. Store the information about our rate limits for each request type in the Postgres DB for each access token.

When we make API requests to the API, rotate which API key we use to evenly deplete each donated API key. Remember that different kind of requests (ex. core vs. GraphQL vs. search) deplete different limiits

On GitHub's end, users can revoke their tokens at any point. This may cause API requests from certain donated tokens to suddenly fail. If a user has revoked their access token, then mark the token as revoked in our DB and don't use it in token rotation. If that user later re-authenticates, then make sure it will work again.

# How caching works

There should be 2 settings for caching:

MAX_CACHE_TIME (default 5 minutes, 0 = unlimited)
MAX_CACHE_SIZE_MB (max size of cache in MB, default = 100. This should reflect the size of the cached responses table on disk)

By default (configurable in env), cache responses from GitHub for MAX_CACHE_TIME or until we reach MAX_CACHE_SIZE_MB for our cached responses table in our DB. Once we hit MAX_CACHE_SIZE_MB, remove old cached responses starting with the oldest.

Have a cleanup job that runs periodically to manage the cache to minimize disk space and resources used. That way our cache should be cleaned up and disk space minimized even if no new API requests come in

# Development and deployment

We will do our development and deployment both in Docker. Development will need a Dockerfile.dev and use docker compose. It should use air to automatically refresh our server when there are changes.

The AI agent should understand that our development environment is in Docker using tools like docker compose. However, we will run the development server itself in a separate window so the AI agent should not try to start / stop the development server directly. Instead it should pull the logs and assume it's running in another window. If it's not running in another window, then it should prompt the user to start the server - and not start it itself.

Deployment will be via Coolify with a Dockerfile (not docker compose).

# GitHub Docs

The GitHub API is complicated. Make sure to consult its docs and run test HTTP requests to carefully understand how API request limits work and how reqeusts are structured.

## Rate Limits

REST API endpoints for rate limits
Use the REST API to check your current rate limit status.

About rate limits
You can check your current rate limit status at any time. For more information about rate limit rules, see Rate limits for the REST API.

The REST API for searching items has a custom rate limit that is separate from the rate limit governing the other REST API endpoints. For more information, see REST API endpoints for search. The GraphQL API also has a custom rate limit that is separate from and calculated differently than rate limits in the REST API. For more information, see Rate limits and node limits for the GraphQL API. For these reasons, the API response categorizes your rate limit. Under resources, you'll see objects relating to different categories:

The core object provides your rate limit status for all non-search-related resources in the REST API.

The search object provides your rate limit status for the REST API for searching (excluding code searches). For more information, see REST API endpoints for search.

The code_search object provides your rate limit status for the REST API for searching code. For more information, see REST API endpoints for search.

The graphql object provides your rate limit status for the GraphQL API.

The integration_manifest object provides your rate limit status for the POST /app-manifests/{code}/conversions operation. For more information, see Registering a GitHub App from a manifest.

The dependency_snapshots object provides your rate limit status for submitting snapshots to the dependency graph. For more information, see REST API endpoints for the dependency graph.

The code_scanning_upload object provides your rate limit status for uploading SARIF results to code scanning. For more information, see Uploading a SARIF file to GitHub. The actions_runner_registration object provides your rate limit status for registering self-hosted runners in GitHub Actions. For more information, see REST API endpoints for self-hosted runners.

For more information on the headers and values in the rate limit response, see Rate limits for the REST API.

Get rate limit status for the authenticated user
Note

Accessing this endpoint does not count against your REST API rate limit.

Some categories of endpoints have custom rate limits that are separate from the rate limit governing the other REST API endpoints. For this reason, the API response categorizes your rate limit. Under resources, you'll see objects relating to different categories:

The core object provides your rate limit status for all non-search-related resources in the REST API.
The search object provides your rate limit status for the REST API for searching (excluding code searches). For more information, see "Search."
The code_search object provides your rate limit status for the REST API for searching code. For more information, see "Search code."
The graphql object provides your rate limit status for the GraphQL API. For more information, see "Resource limitations."
The integration_manifest object provides your rate limit status for the POST /app-manifests/{code}/conversions operation. For more information, see "Creating a GitHub App from a manifest."
The dependency_snapshots object provides your rate limit status for submitting snapshots to the dependency graph. For more information, see "Dependency graph."
The dependency_sbom object provides your rate limit status for requesting SBOMs from the dependency graph. For more information, see "Dependency graph."
The code_scanning_upload object provides your rate limit status for uploading SARIF results to code scanning. For more information, see "Uploading a SARIF file to GitHub."
The actions_runner_registration object provides your rate limit status for registering self-hosted runners in GitHub Actions. For more information, see "Self-hosted runners."
The source_import object is no longer in use for any API endpoints, and it will be removed in the next API version. For more information about API versions, see "API Versions."
Note

The rate object is closing down. If you're writing new API client code or updating existing code, you should use the core object instead of the rate object. The core object contains the same information that is present in the rate object.

Fine-grained access tokens for "Get rate limit status for the authenticated user"
This endpoint works with the following fine-grained token types:

GitHub App user access tokens
GitHub App installation access tokens
Fine-grained personal access tokens
The fine-grained token does not require any permissions.

This endpoint can be used without authentication if only public resources are requested.

HTTP response status codes for "Get rate limit status for the authenticated user"
Status code	Description
200	
OK

304	
Not modified

404	
Resource not found

Code samples for "Get rate limit status for the authenticated user"
Request example
get
/rate_limit
cURL
JavaScript
GitHub CLI
curl -L \
  -H "Accept: application/vnd.github+json" \
  -H "X-GitHub-Api-Version: 2022-11-28" \
  https://api.github.com/rate_limit
Response

Example response
Response schema
Status: 200
{
  "resources": {
    "core": {
      "limit": 5000,
      "used": 1,
      "remaining": 4999,
      "reset": 1691591363
    },
    "search": {
      "limit": 30,
      "used": 12,
      "remaining": 18,
      "reset": 1691591091
    },
    "graphql": {
      "limit": 5000,
      "used": 7,
      "remaining": 4993,
      "reset": 1691593228
    },
    "integration_manifest": {
      "limit": 5000,
      "used": 1,
      "remaining": 4999,
      "reset": 1691594631
    },
    "source_import": {
      "limit": 100,
      "used": 1,
      "remaining": 99,
      "reset": 1691591091
    },
    "code_scanning_upload": {
      "limit": 500,
      "used": 1,
      "remaining": 499,
      "reset": 1691594631
    },
    "actions_runner_registration": {
      "limit": 10000,
      "used": 0,
      "remaining": 10000,
      "reset": 1691594631
    },
    "scim": {
      "limit": 15000,
      "used": 0,
      "remaining": 15000,
      "reset": 1691594631
    },
    "dependency_snapshots": {
      "limit": 100,
      "used": 0,
      "remaining": 100,
      "reset": 1691591091
    },
    "code_search": {
      "limit": 10,
      "used": 0,
      "remaining": 10,
      "reset": 1691591091
    },
    "code_scanning_autofix": {
      "limit": 10,
      "used": 0,
      "remaining": 10,
      "reset": 1691591091
    }
  },
  "rate": {
    "limit": 5000,
    "used": 1,
    "remaining": 4999,
    "reset": 1372700873
  }
}
