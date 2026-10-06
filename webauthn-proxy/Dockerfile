# The builder runs on the CI host's own architecture and cross-compiles for the
# target, so multi-arch builds need no QEMU emulation.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS builder
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /webauthn-proxy ./cmd/webauthn-proxy

# gcr.io/distroless/static-debian12 contains only ca-certificates and tzdata —
# no shell, no package manager, no libc.  The attack surface is minimal.
# Pin to a specific digest for reproducible builds; refresh with `task update-distroless`.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /webauthn-proxy /webauthn-proxy
VOLUME ["/data"]
EXPOSE 4180
ENTRYPOINT ["/webauthn-proxy"]
