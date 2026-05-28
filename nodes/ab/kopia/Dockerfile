FROM kopia/kopia:latest AS kopia-src

FROM debian:bookworm-slim
RUN apt-get update \
  && apt-get install -y --no-install-recommends postgresql-client ca-certificates \
  && rm -rf /var/lib/apt/lists/*
COPY --from=kopia-src /app/kopia /app/kopia
COPY --from=kopia-src /app/rclone /app/rclone
ENTRYPOINT ["/app/kopia"]
