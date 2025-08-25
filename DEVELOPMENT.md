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

#### 2. Production Mode ⭐ RECOMMENDED
```bash
# Production: Let logs go to stdout/stderr (DO THIS)
./bin/server

# The application logs naturally to stderr, which is perfect for:
# - Docker: docker logs container-name
# - Kubernetes: kubectl logs pod-name  
# - systemd: journalctl -u service-name
# - Process managers: PM2, supervisor handle log aggregation
```

#### 3. Legacy/Development File Logging (NOT recommended for production)
```bash
# Only use for local development if you need persistent files
./bin/server 2>&1 | tee server.log &
```

#### 4. Docker Development
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

### 🚀 Production Deployment

The application is designed for modern deployment patterns where logs go to stdout/stderr:

#### Docker
```dockerfile
# Dockerfile example
FROM golang:1.21-alpine AS builder
WORKDIR /app
COPY . .
RUN go build -o server ./cmd/server

FROM alpine:latest
RUN apk --no-cache add ca-certificates
WORKDIR /root/
COPY --from=builder /app/server .
CMD ["./server"]  # Logs naturally go to stdout
```

```bash
# View logs
docker logs container-name
docker logs -f container-name  # follow
```

#### systemd Service
```ini
# /etc/systemd/system/gh-proxy.service
[Unit]
Description=GitHub Proxy Service
After=network.target

[Service]
Type=simple
User=ghproxy
WorkingDirectory=/opt/gh-proxy
ExecStart=/opt/gh-proxy/server
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

```bash
# View logs
journalctl -u gh-proxy
journalctl -u gh-proxy -f  # follow
```

#### Kubernetes
```yaml
# deployment.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: gh-proxy
spec:
  containers:
  - name: gh-proxy
    image: gh-proxy:latest
    # Logs automatically captured by Kubernetes
```

```bash
# View logs  
kubectl logs deployment/gh-proxy
kubectl logs -f deployment/gh-proxy  # follow
```

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
- **Production**: Use `./bin/server` (logs to stdout, no files needed)
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
