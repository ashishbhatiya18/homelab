# StackMgr Proxy - Backend

Go REST API for LocalStack management with lifecycle operations and health checks.

## Features

- **Stack Management**: Start, stop, restart, rebuild, and update stacks
- **Health Checks**: Generic and service-specific health monitoring
- **Logs**: Retrieve stack logs with configurable line count
- **Auth Integration**: Redirect to `auth.ab18.in` for authentication

## Environment Variables

- `PORT`: Server port (default: 8080)
- `STACKS_PATH`: Path to stack definitions (default: /localstack/nodes)
- `AUTH_URL`: Authentication service URL (default: https://auth.ab18.in)
- `DOCKER_SOCK`: Docker socket path (default: /var/run/docker.sock)

## API Endpoints

### Health
- `GET /api/health` - System health status
- `GET /api/stacks/:environment/:stack/health` - Stack-specific health

### Management
- `GET /api/stacks` - List all stacks
- `GET /api/stacks/:environment/:stack` - Get stack details
- `POST /api/stacks/:environment/:stack/start` - Start stack
- `POST /api/stacks/:environment/:stack/stop` - Stop stack
- `POST /api/stacks/:environment/:stack/restart` - Restart stack
- `POST /api/stacks/:environment/:stack/rebuild` - Rebuild stack
- `POST /api/stacks/:environment/:stack/update` - Update stack

### Logs
- `GET /api/stacks/:environment/:stack/logs?lines=100` - Get logs

## Running Locally

```bash
cd backend
go mod download
go run main.go
```

## Docker Build

```bash
docker build -t stackmgr-proxy:latest .
docker run -p 8080:8080 -v /var/run/docker.sock:/var/run/docker.sock stackmgr-proxy
```
