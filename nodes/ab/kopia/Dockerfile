FROM kopia/kopia:latest AS kopia-src

FROM debian:bookworm-slim
# pg_dump from the PostgreSQL apt repository: it must be at least the server's
# major version (Debian bookworm's own client is 15).
ARG PG_MAJOR=18
ADD https://www.postgresql.org/media/keys/ACCC4CF8.asc /usr/share/keyrings/pgdg.asc
RUN apt-get update \
  && apt-get install -y --no-install-recommends ca-certificates \
  && chmod 644 /usr/share/keyrings/pgdg.asc \
  && echo "deb [signed-by=/usr/share/keyrings/pgdg.asc] https://apt.postgresql.org/pub/repos/apt bookworm-pgdg main" > /etc/apt/sources.list.d/pgdg.list \
  && apt-get update \
  && apt-get install -y --no-install-recommends postgresql-client-${PG_MAJOR} \
  && rm -rf /var/lib/apt/lists/*
COPY --from=kopia-src /usr/bin/kopia /usr/bin/kopia
COPY --from=kopia-src /usr/bin/rclone /usr/bin/rclone
COPY entrypoint.sh /entrypoint.sh
RUN mkdir -p /scripts
ENTRYPOINT ["/entrypoint.sh"]
