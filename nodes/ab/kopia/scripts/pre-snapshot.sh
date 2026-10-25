#!/bin/sh
set -e

DUMP_DIR=/data-ab/data/pg_dumps
# The postgres password: the file PG_PASSWORD_FILE names, or PG_PASSWORD.
if [ -n "${PG_PASSWORD_FILE:-}" ]; then
  PG_PASSWORD=$(cat "$PG_PASSWORD_FILE")
fi
mkdir -p "$DUMP_DIR"

for db in ab18 citrusdental photos memos; do
  PGPASSWORD="$PG_PASSWORD" pg_dump \
    -h postgres \
    -U postgres \
    -Fc \
    -d "$db" \
    -f "$DUMP_DIR/${db}.pgdump"
done
