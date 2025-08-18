ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS key_hash TEXT;
ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS key_prefix TEXT;
-- backfill if old key column exists
UPDATE api_keys SET key_hash = encode(digest(key, 'sha256'),'hex') WHERE key_hash IS NULL AND key IS NOT NULL;
UPDATE api_keys SET key_prefix = substr(key,1,5) WHERE key_prefix IS NULL AND key IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_api_keys_key_hash ON api_keys(key_hash);
