# ── Stage 1: Build Next.js frontend ──────────────────────────────────────────
FROM node:20-alpine AS frontend
WORKDIR /app
COPY frontend/package.json frontend/package-lock.json* frontend/yarn.lock* ./
RUN npm ci --ignore-scripts
COPY frontend/ ./
# Empty string → relative URLs → served from the same Go origin
ARG NEXT_PUBLIC_API_URL=""
ENV NEXT_PUBLIC_API_URL=$NEXT_PUBLIC_API_URL
RUN npm run build
# next.config has output:"export" so the static site lands in out/

# ── Stage 2: Build Go backend with embedded frontend ─────────────────────────
FROM golang:1.25-alpine AS backend
WORKDIR /app
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
# Embed the Next.js static export so the binary serves it directly
COPY --from=frontend /app/out ./static
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w" -trimpath -o stackmgr-proxy .

# ── Stage 3: Distroless runtime ───────────────────────────────────────────────
# static-debian12 has no shell, no package manager — minimal attack surface.
# The Go binary is statically linked (CGO_ENABLED=0) so no glibc needed.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=backend /app/stackmgr-proxy /stackmgr-proxy
EXPOSE 8080
ENTRYPOINT ["/stackmgr-proxy"]
