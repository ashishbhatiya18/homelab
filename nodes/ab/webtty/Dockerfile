# Stage 1: Build the self-hosted frontend bundle (zero runtime CDN).
FROM node:22-bookworm-slim AS web-builder
WORKDIR /build/web
COPY web/package.json web/package-lock.json* ./
RUN npm ci
COPY web/ ./
RUN npm run build

# Stage 2: Build the static musl Rust binary embedding web/dist.
FROM rust:1-alpine AS rust-builder
RUN apk add --no-cache build-base ca-certificates binutils
ENV RUSTFLAGS="-C target-feature=+crt-static"
WORKDIR /build
COPY --from=web-builder /build/web/dist ./web/dist
COPY server/Cargo.toml server/Cargo.lock* ./server/
COPY server/src ./server/src
WORKDIR /build/server
RUN cargo build --release \
    && ! readelf -l target/release/webtty | grep -q INTERP

# Create minimal non-root passwd/group entries for FROM scratch.
RUN printf 'app:x:1000:1000:app:/:/sbin/nologin\n' > /tmp/passwd \
    && printf 'app:x:1000:\n' > /tmp/group

# Stage 3: True FROM scratch distroless runtime.
FROM scratch
COPY --from=rust-builder /tmp/passwd /etc/passwd
COPY --from=rust-builder /tmp/group /etc/group
COPY --from=rust-builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=rust-builder /build/server/target/release/webtty /webtty

USER 1000:1000
ENV PORT=7681
EXPOSE 7681
ENTRYPOINT ["/webtty"]
