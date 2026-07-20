#!/bin/sh
set -eu

: "${FAIRY_DATABASE_URL:?FAIRY_DATABASE_URL is required}"

if [ "$#" -ne 1 ] || [ ! -f "$1" ]; then
  echo "usage: restore-postgres.sh /absolute/path/fairy.dump" >&2
  exit 2
fi

case "$1" in
  /*) ;;
  *) echo "backup path must be absolute" >&2; exit 2 ;;
esac

existing="$(psql "$FAIRY_DATABASE_URL" -Atqc "SELECT count(*) FROM pg_tables WHERE schemaname = current_schema() AND tablename <> 'fairy_schema_migrations'")"
if [ "$existing" -ne 0 ]; then
  echo "restore target schema must be empty" >&2
  exit 1
fi

pg_restore --dbname="$FAIRY_DATABASE_URL" --no-owner --no-acl --single-transaction "$1"
psql "$FAIRY_DATABASE_URL" -v ON_ERROR_STOP=1 -Atqc "SELECT max(version) FROM fairy_schema_migrations"
echo "PostgreSQL restore completed; run fairy db status before cutover"
