FROM kopia/kopia:latest AS kopia-src

FROM debian:bookworm-slim
RUN apt-get update \
  && apt-get install -y --no-install-recommends postgresql-client ca-certificates \
  && rm -rf /var/lib/apt/lists/*
COPY --from=kopia-src /usr/bin/kopia /usr/bin/kopia
COPY --from=kopia-src /usr/bin/rclone /usr/bin/rclone
RUN mkdir -p /scripts
ENTRYPOINT ["/usr/bin/kopia"]
