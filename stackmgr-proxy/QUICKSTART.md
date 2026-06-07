# Quick Start Guide - StackMgr Proxy

## 5-Minute Setup

### Prerequisites

- Go 1.21+
- Node.js 18+
- Docker & Docker Compose
- Auth token from auth.ab18.in

### Option 1: Docker Compose (Recommended)

```bash
# Clone or navigate to project
cd /Users/ab18/Desktop/Projects/stackmgr-proxy

# Run both services
docker-compose up -d

# Check services
docker-compose ps

# View logs
docker-compose logs -f
```

Access at: **http://localhost:3000**

### Option 2: Local Development

#### Terminal 1 - Backend
```bash
cd backend
cp .env.example .env
# Edit .env if needed
go mod download
go run main.go
```

#### Terminal 2 - Frontend
```bash
cd frontend
cp .env.local.example .env.local
npm install
npm run dev
```

Access at: **http://localhost:3000**

### Option 3: Using Make

```bash
# First time setup
make setup

# Run in development
make backend-dev  # Terminal 1
make frontend-dev # Terminal 2

# Or run with docker
make run
make stop
```

## Next Steps

1. **Configure Auth**
   - Update `AUTH_URL` in backend .env
   - Update `NEXT_PUBLIC_AUTH_URL` in frontend .env.local

2. **Verify Services Discovery**
   - Backend should auto-discover services from `/localstack/nodes`
   - Visit http://localhost:8080/api/stacks to see discovered stacks

3. **Check Health**
   - Visit http://localhost:8080/api/health
   - Should return system health status

4. **Test Dashboard**
   - Open http://localhost:3000
   - Should redirect to auth service if not authenticated
   - After auth, should show list of stacks

## Common Issues

### "Docker socket permission denied"
```bash
sudo usermod -aG docker $USER
newgrp docker
```

### "Cannot find stacks"
- Verify `/localstack/nodes` exists and contains subdirectories
- Check backend logs: `docker-compose logs backend`

### "Auth redirect loop"
- Verify auth.ab18.in is accessible
- Check auth token is being stored properly
- See browser DevTools → Application → Storage

### "Port already in use"
```bash
# Change ports in docker-compose.yml or:
make stop  # if running with compose
```

## Important Files

- `backend/main.go` - API endpoints & logic
- `frontend/src/app/page.tsx` - Main dashboard
- `docker-compose.yml` - Local deployment config
- `DEPLOYMENT.md` - Production deployment guide

## API Documentation

See [backend/README.md](backend/README.md) for full API reference.

**Quick endpoints:**
- `GET /api/health` - System status
- `GET /api/stacks` - List all stacks
- `POST /api/stacks/ab/immich/start` - Start a service

## Deployment

For production deployment to:
- **Backend**: Docker container on your ab node
- **Frontend**: Cloudflare Pages

See [DEPLOYMENT.md](DEPLOYMENT.md)

## Support

Check logs for debugging:
```bash
# Docker Compose
docker-compose logs -f

# Specific service
docker-compose logs -f backend
docker-compose logs -f frontend

# Local Go
go run main.go  # Shows output directly
```

## Next: Real Implementation

The current implementation has placeholder handlers for lifecycle operations. To enable actual stack control:

1. **Docker Integration** (backend/main.go)
   - Replace handlers with actual docker-compose calls
   - Use Docker client SDK or shell exec

2. **Health Checks** (backend/main.go)
   - Implement HTTP health checks for each service
   - Add service-specific check logic

3. **Logging**
   - Stream docker compose logs endpoint
   - Add filtering and search

See inline TODOs in `backend/main.go`.
