version: '3.8'

# This is the root README for the stackmgr-proxy project

# StackMgr Proxy - Stack Management & Health Monitoring

Complete solution for managing LocalStack services with a Go backend API and modern React frontend.

## Architecture

```
┌─────────────────────┐
│   Cloudflare Pages  │
│    (Frontend SSG)   │
└──────────┬──────────┘
           │
           ↓
┌─────────────────────┐       ┌──────────────────┐
│  auth.ab18.in       │←──────│  StackMgr Proxy  │
│  (Auth Service)     │       │  (Backend API)   │
└─────────────────────┘       └────────┬─────────┘
                                       │
                                       ↓
                              ┌──────────────────┐
                              │  Docker Daemon   │
                              │  /var/run/docker │
                              └──────────────────┘
```

## Components

### Backend (`/backend`)

Go REST API with:
- Stack lifecycle management (start, stop, restart, rebuild, update)
- Health checks (generic + service-specific)
- Log streaming
- Docker Compose integration
- Auth token validation

**Key Endpoints:**
- `GET /api/health` - System health
- `GET /api/stacks` - List all stacks
- `POST /api/stacks/:env/:stack/start` - Start stack
- `POST /api/stacks/:env/:stack/logs` - Get logs

### Frontend (`/frontend`)

Next.js SSG application with:
- Dashboard showing all stacks by environment
- Real-time health monitoring
- Stack control buttons (start, stop, restart, rebuild, update, logs)
- Auth integration with redirect to auth.ab18.in
- Responsive design for Cloudflare Pages

## Quick Start

### Local Development

```bash
# Terminal 1 - Backend
cd backend
go mod download
go run main.go

# Terminal 2 - Frontend
cd frontend
npm install
npm run dev
```

Access dashboard at: http://localhost:3000

### Docker Compose

```bash
docker-compose up -d
```

Access at: http://localhost:3000

## Configuration

### Backend (.env)

```env
PORT=8080
STACKS_PATH=/localstack/nodes
AUTH_URL=https://auth.ab18.in
DOCKER_SOCK=/var/run/docker.sock
```

### Frontend (.env.local)

```env
NEXT_PUBLIC_API_URL=https://api.example.com
NEXT_PUBLIC_AUTH_URL=https://auth.ab18.in
```

## Deployment

### Backend

Deploy as Docker container with:
- Docker socket mount
- Localstack volumes access
- Environment variables configured

### Frontend

Deploy to Cloudflare Pages:

1. Connect GitHub repo
2. Build command: `npm run build`
3. Publish directory: `out`
4. Set environment variables in Cloudflare Pages UI

## Service Health Checks

Supports service-specific health endpoints for:

- **immich**: HTTP ping to :2283/api/server/ping
- **vaultwarden**: HTTP check to :80/alive
- **ollama**: GET :11434/api/tags
- **syncthing**: GET :8384/rest/system/status
- **homeautomation**: GET :8123/api/
- And more...

## Stack Support

Manages services in:
- `nodes/ab/` - Primary environment
- `nodes/cd/` - Secondary environment

Each environment can contain multiple stacks for independent lifecycle management.

## Security

- Authentication required via auth.ab18.in
- Token-based API access
- Auth redirect middleware
- Tokens passed in Authorization header or cookies

## Development

### Adding a new service health check

Edit [backend/main.go](backend/main.go) `getHealthCheckEndpoints()`:

```go
func (sm *StackManager) getHealthCheckEndpoints(stack string) map[string]string {
    endpoints := map[string]string{
        "myservice": "http://myservice:8080/health",
    }
    return endpoints
}
```

### Frontend styling

Uses Tailwind CSS with custom component utilities in [frontend/src/app/globals.css](frontend/src/app/globals.css).

## License

Part of the ab18 infrastructure.
