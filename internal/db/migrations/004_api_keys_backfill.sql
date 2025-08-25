-- backfill hashes and prefixes if legacy plaintext column exists
UPDATE api_keys SET key_hash = encode(digest(key, 'sha256'),'hex')
WHERE key_hash IS NULL AND key IS NOT NULL AND EXISTS (
  SELECT 1 FROM information_schema.columns WHERE table_name='api_keys' AND column_name='key'
);

UPDATE api_keys SET key_prefix = substr(key,1,5)
WHERE key_prefix IS NULL AND key IS NOT NULL AND EXISTS (
  SELECT 1 FROM information_schema.columns WHERE table_name='api_keys' AND column_name='key'
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_api_keys_key_hash ON api_keys(key_hash);
