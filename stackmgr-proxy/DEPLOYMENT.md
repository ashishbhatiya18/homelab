# StackMgr Proxy - Deployment Guide

## Production Deployment Architecture

```
┌────────────────────────────────────────┐
│       Internet / Tailscale             │
└──────────────────┬─────────────────────┘
                   │
        ┌──────────┴──────────┐
        │                     │
   ┌────▼────┐          ┌────▼────┐
   │ Proxies │          │Frontend  │
   │(auth)   │          │(Pages)   │
   └────┬────┘          └────┬────┘
        │                    │
        └──────────┬─────────┘
                   │
             ┌─────▼──────┐
             │ StackMgr   │
             │ Backend    │
             │ (Docker)   │
             └─────┬──────┘
                   │
            ┌──────▼────────┐
            │ Docker Daemon │
            │ + Compose     │
            └───────────────┘
```

## Deployment Steps

### 1. Backend Deployment

The backend should run on a machine with Docker access (e.g., your ab node).

#### Option A: Docker Container

```bash
# Build image
docker build -t stackmgr-proxy:latest ./backend

# Run container
docker run -d \
  --name stackmgr-proxy \
  -p 8080:8080 \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v /localstack:/localstack:ro \
  -e STACKS_PATH=/localstack/nodes \
  -e AUTH_URL=https://auth.ab18.in \
  --restart unless-stopped \
  stackmgr-proxy:latest
```

#### Option B: Systemd Service

Create `/etc/systemd/system/stackmgr-proxy.service`:

```ini
[Unit]
Description=StackMgr Proxy
After=docker.service
Requires=docker.service

[Service]
Type=simple
User=root
WorkingDirectory=/opt/stackmgr-proxy/backend
Environment="STACKS_PATH=/localstack/nodes"
Environment="AUTH_URL=https://auth.ab18.in"
Environment="PORT=8080"
ExecStart=/usr/bin/stackmgr-proxy
Restart=always
RestartSec=10

[Install]
WantedBy=multi-user.target
```

Then:
```bash
sudo systemctl enable stackmgr-proxy
sudo systemctl start stackmgr-proxy
```

#### Option C: Docker Compose

```bash
# In localstack directory
docker-compose -f stackmgr-proxy/docker-compose.yml up -d backend
```

### 2. Frontend Deployment to Cloudflare Pages

#### Prerequisites

- GitHub repository connected to Cloudflare Pages
- Cloudflare account

#### Setup

1. **Push to GitHub**
   ```bash
   git remote add origin https://github.com/your-org/stackmgr-proxy.git
   git push -u origin main
   ```

2. **Connect to Cloudflare Pages**
   - Go to Cloudflare Dashboard
   - Pages → Create a project
   - Select GitHub repository
   - Configure build:
     - **Build command**: `npm run build`
     - **Build output directory**: `out`

3. **Set Environment Variables**
   - In Cloudflare Pages settings, add variables:
     ```
     NEXT_PUBLIC_API_URL = https://stackmgr-api.your-domain.com
     NEXT_PUBLIC_AUTH_URL = https://auth.ab18.in
     ```

4. **Custom Domain**
   - Add custom domain in Pages settings
   - Point DNS to Cloudflare

### 3. API Reverse Proxy

You may want to reverse proxy the backend through your existing infrastructure:

#### Using Nginx

```nginx
server {
    listen 443 ssl http2;
    server_name stackmgr-api.your-domain.com;

    ssl_certificate /path/to/cert;
    ssl_certificate_key /path/to/key;

    location / {
        proxy_pass http://localhost:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

#### Using Caddy

```caddy
stackmgr-api.your-domain.com {
    reverse_proxy localhost:8080
}
```

### 4. Monitoring & Logs

#### View backend logs
```bash
# Docker
docker logs -f stackmgr-proxy

# Systemd
journalctl -fu stackmgr-proxy

# Docker Compose
docker-compose logs -f backend
```

#### Check health
```bash
curl -H "Authorization: Bearer YOUR_TOKEN" \
  https://stackmgr-api.your-domain.com/api/health
```

## Security Considerations

1. **API Token Management**
   - Tokens from auth.ab18.in should be validated server-side
   - Implement token caching/validation in middleware

2. **Docker Socket Access**
   - Limit container permissions
   - Use read-only access where possible

3. **CORS**
   - Configure CORS in backend if frontend on different domain
   - Add to `main.go`:
   ```go
   router.Use(cors.Default())
   ```

4. **Rate Limiting**
   - Implement rate limits on lifecycle operations
   - Prevent accidental cascade failures

## Scaling Considerations

For multiple environments or larger deployments:

1. **Multiple Backend Instances**
   - Use load balancer
   - Implement state storage for concurrent operations

2. **Caching**
   - Cache health check results
   - Implement periodic refresh

3. **Message Queue**
   - For long-running operations (rebuild, update)
   - Use task queue (e.g., RabbitMQ, Redis)

## Troubleshooting

### Backend fails to start
- Check Docker socket permissions: `ls -la /var/run/docker.sock`
- Verify user permissions: `docker ps` should work without sudo

### API returns 401 Unauthorized
- Check auth token in request header
- Verify auth service is accessible
- Check token expiration

### Frontend can't reach API
- Check CORS settings
- Verify backend URL in environment variables
- Check network connectivity

### Services not showing in list
- Verify STACKS_PATH points to correct localstack directory
- Check directory structure: `ls -la /localstack/nodes/ab/`
- Review backend logs for parsing errors
