#!/bin/sh
set -e

DUMP_DIR=/data-ab/data/pg_dumps
mkdir -p "$DUMP_DIR"

for db in ab18 citrusdental photos; do
  PGPASSWORD="$PG_PASSWORD" pg_dump \
    -h postgres \
    -U postgres \
    -Fc \
    -d "$db" \
    -f "$DUMP_DIR/${db}.pgdump"
done
