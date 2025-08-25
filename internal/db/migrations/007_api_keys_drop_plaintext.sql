-- Remove any old plaintext keys and drop the column
UPDATE api_keys SET key = NULL;
ALTER TABLE api_keys DROP COLUMN IF EXISTS key;
