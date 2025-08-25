# Token Migration Guide

This guide helps you migrate donated GitHub tokens from dev to production database.

## 🎯 What This Does

Copies all donated GitHub tokens from your development database to production database, **excluding zachlatta's tokens**.

**Migration Summary:**
- **Source**: Development database (119 non-zachlatta tokens)
- **Target**: Production database 
- **Exclusions**: Any tokens where `github_user = 'zachlatta'`
- **Safety**: Uses `ON CONFLICT` to handle duplicates

## 🚀 How to Run Migration

### Prerequisites
1. ✅ `PROD_DB_URL` set in your `.env` file
2. ✅ Development database running (docker-compose)
3. ✅ Production database accessible and migrated

### Step 1: Run the Migration

```bash
# Load environment variables and run migration
source .env && ./bin/migrate-tokens
```

### Step 2: Verify Migration

```bash
# Verify the migration worked correctly
source .env && ./bin/verify-migration
```

## 📊 Expected Output

### Migration Output
```
🔄 Starting token migration from dev to prod...
📡 Connecting to dev database...
📡 Connecting to prod database...
✅ Database connections successful
📊 Found 119 non-zachlatta active tokens in dev database
📊 Current prod database has 0 non-zachlatta tokens
🔄 Starting token migration...
✅ Migrated token for @maxwofford
✅ Migrated token for @3kh0
✅ Migrated token for @arctixdev
... (continues for all tokens)
🎉 Migration completed!
✅ Successfully migrated: 119 tokens
📊 Final prod database count: 119 non-zachlatta tokens
🚀 Token migration complete!
```

### Verification Output
```
🔍 Verifying token migration...
📊 Dev database: 119 active non-zachlatta tokens
📊 Prod database: 119 non-zachlatta tokens
✅ Migration appears successful! Prod has 119 tokens (>= dev's 119)

📋 Sample migrated users in production:
  • @GamerC0der (created: 2025-05-04)
  • @xndadelin (created: 2025-04-28)
  • @Daamin909 (created: 2025-03-03)
  ...
🎉 Verification complete!
```

## 🔒 Safety Features

- **Transactional**: All changes in a single transaction (rollback on failure)
- **Duplicate Handling**: Uses `ON CONFLICT` to update existing records
- **Exclusion Filter**: Automatically excludes zachlatta's tokens
- **Progress Logging**: Shows each token as it's migrated
- **Verification**: Counts and confirms final migration

## 🗃️ Database Schema

The migration copies these fields from `donated_tokens` table:
- `id` (UUID)
- `github_user` (username)  
- `token` (GitHub API token)
- `created_at` (timestamp)
- `revoked` (boolean)
- `last_ok_at` (timestamp)
- `scopes` (GitHub scopes)

## ⚠️ Important Notes

1. **zachlatta's tokens are NOT migrated** (as requested)
2. **Existing tokens in prod will be updated** if duplicates exist
3. **Only active tokens** (`revoked = false`) are migrated from dev
4. **All token states** (active/revoked) are preserved in prod

## 🧹 Cleanup

After successful migration, you can optionally:
- Remove the migration utilities: `rm -f ./bin/migrate-tokens ./bin/verify-migration`
- Remove source code: `rm -rf ./cmd/migrate-tokens ./cmd/verify-migration`
- Keep this documentation for reference

## 🆘 Troubleshooting

### Common Issues

**PROD_DB_URL not set**
```bash
source .env  # Make sure to load environment variables
```

**Connection failed**
- Verify `PROD_DB_URL` is correct
- Ensure production database is accessible
- Check that migrations have been run on production

**Migration fails mid-way**
- Transaction will rollback automatically
- Safe to re-run the migration command
- Check database connectivity and permissions
