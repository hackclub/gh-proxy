CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE IF NOT EXISTS donated_tokens (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  github_user TEXT NOT NULL,
  token TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  revoked BOOLEAN NOT NULL DEFAULT false,
  last_ok_at TIMESTAMPTZ,
  scopes TEXT,
  UNIQUE (github_user)
);

CREATE TABLE IF NOT EXISTS token_rate_limits (
  token_id UUID REFERENCES donated_tokens(id) ON DELETE CASCADE,
  category TEXT NOT NULL, -- core, search, code_search, graphql, etc
  rate_limit INT NOT NULL,
  remaining INT NOT NULL,
  reset TIMESTAMPTZ NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (token_id, category)
);

CREATE TABLE IF NOT EXISTS api_keys (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  key TEXT NOT NULL UNIQUE,
  hc_username TEXT NOT NULL,
  app_name TEXT NOT NULL,
  machine TEXT NOT NULL,
  rate_limit_per_sec INT NOT NULL DEFAULT 10,
  disabled BOOLEAN NOT NULL DEFAULT false,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_used_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS cached_responses (
  id BIGSERIAL PRIMARY KEY,
  method TEXT NOT NULL,
  url TEXT NOT NULL,
  req_body BYTEA,
  status INT NOT NULL,
  resp_headers JSONB NOT NULL,
  resp_body BYTEA NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  expires_at TIMESTAMPTZ,
  content_hash TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_cached_responses_lookup ON cached_responses(method, url, content_hash);
CREATE INDEX IF NOT EXISTS idx_cached_responses_created_at ON cached_responses(created_at);

CREATE TABLE IF NOT EXISTS request_logs (
  id BIGSERIAL PRIMARY KEY,
  api_key TEXT,
  method TEXT NOT NULL,
  path TEXT NOT NULL,
  status INT NOT NULL,
  cache_hit BOOLEAN NOT NULL DEFAULT false,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_request_logs_apikey_created ON request_logs(api_key, created_at);
