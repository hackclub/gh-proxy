# Development Guide

## Running the Server

### 🚀 Quick Start
```bash
# Build the server
go build -o ./bin/server ./cmd/server

# Run in development (logs to terminal)
./bin/server
```

### 📊 Logging Options

#### 1. Development Mode (Recommended)
```bash
# Foreground with logs in terminal (Ctrl+C to stop)
./bin/server

# Background with logs in terminal
./bin/server &
```

#### 2. Production Mode
```bash
# Background with logs to file only
./bin/server > server.log 2>&1 &

# Background with logs to BOTH terminal and file
./bin/server 2>&1 | tee server.log &
```

#### 3. Docker Development
```bash
# Using docker-compose (logs to stdout/docker logs)
docker-compose up app

# View logs
docker-compose logs -f app
```

### 🔍 Log Contents

The application logs include:
- **Startup**: Server listening confirmation
- **Requests**: `GET /path 200 (123ms) ua="..." key="..."`  
- **Admin Actions**: API key creation, token management
- **OAuth Flow**: Login redirects, token storage
- **Errors**: Database issues, proxy failures
- **Cache**: Hit/miss statistics, cleanup operations

### 🛠️ Management Commands

```bash
# Check if server is running
pgrep -f 'bin/server'

# Stop the server
pkill -f 'bin/server'

# Follow logs (when using file)
tail -f server.log

# View recent logs
tail -20 server.log

# Search logs
grep "ERROR" server.log
```

### 📍 Endpoints

- **Homepage**: http://localhost:8080/
- **API Docs**: http://localhost:8080/docs
- **Admin Panel**: http://localhost:8080/admin (admin:admin)
- **GitHub Proxy**: http://localhost:8080/gh/*

### 🔧 Configuration

The server uses environment variables (or `.env` file):

```env
DATABASE_URL=postgres://ghproxy:ghproxy@localhost:5433/ghproxy?sslmode=disable
BASE_URL=http://localhost:8080
ADMIN_USER=admin
ADMIN_PASS=admin
GITHUB_OAUTH_CLIENT_ID=your_client_id
GITHUB_OAUTH_CLIENT_SECRET=your_client_secret
MAX_CACHE_TIME=300
MAX_CACHE_SIZE_MB=100
MAX_PROXY_BODY_BYTES=1048576
```

### 🐛 Development Tips

- **Real-time logs**: Use `./bin/server` (foreground) for active development
- **Background development**: Use `./bin/server &` to free up terminal  
- **Production testing**: Use `./bin/server 2>&1 | tee server.log &` for persistent logs
- **Database**: Run `docker-compose up -d db` to start PostgreSQL
- **Hot reload**: Use tools like `air` for auto-restart on changes

### 📝 Log Analysis

```bash
# Request patterns
grep "GET\|POST" server.log | tail -20

# Error monitoring  
grep -i "error\|fail" server.log

# Performance tracking
grep "ms)" server.log | grep -o "([0-9]*ms)" | sort -n

# API key usage
grep "key=" server.log | cut -d'=' -f3 | sort | uniq -c
```
