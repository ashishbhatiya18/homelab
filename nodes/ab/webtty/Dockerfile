# Stage 1: Build the self-hosted frontend bundle (zero runtime CDN).
FROM node:22-bookworm-slim AS web-builder
WORKDIR /build/web
COPY web/package.json web/package-lock.json* ./
RUN npm ci
COPY web/ ./
RUN npm run build

# Stage 2: Build the static musl Rust binary embedding web/dist.
# Using Debian bookworm host (gnu) + musl target keeps proc-macros dynamically
# linked on the host while producing a 100% static musl target binary.
FROM rust:1-bookworm AS rust-builder
RUN apt-get update && apt-get install -y --no-install-recommends \
        musl-tools \
        binutils \
        ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && rustup target add x86_64-unknown-linux-musl
ENV CC_x86_64_unknown_linux_musl=musl-gcc
WORKDIR /build
COPY --from=web-builder /build/web/dist ./web/dist
COPY server/Cargo.toml server/Cargo.lock* ./server/
COPY server/src ./server/src
WORKDIR /build/server
RUN cargo build --release --target x86_64-unknown-linux-musl \
    && ! readelf -l target/x86_64-unknown-linux-musl/release/webtty | grep -q INTERP

# Create minimal non-root passwd/group entries for FROM scratch.
RUN printf 'app:x:1000:1000:app:/:/sbin/nologin\n' > /tmp/passwd \
    && printf 'app:x:1000:\n' > /tmp/group

# Stage 3: True FROM scratch distroless runtime.
FROM scratch
COPY --from=rust-builder /tmp/passwd /etc/passwd
COPY --from=rust-builder /tmp/group /etc/group
COPY --from=rust-builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=rust-builder /build/server/target/x86_64-unknown-linux-musl/release/webtty /webtty

USER 1000:1000
ENV PORT=7681
EXPOSE 7681
ENTRYPOINT ["/webtty"]
