#!/bin/sh
set -eu

: "${FAIRY_DATABASE_URL:?FAIRY_DATABASE_URL is required}"

if [ "$#" -ne 1 ] || [ -z "$1" ]; then
  echo "usage: backup-postgres.sh /absolute/path/fairy.dump" >&2
  exit 2
fi

case "$1" in
  /*) ;;
  *) echo "backup path must be absolute" >&2; exit 2 ;;
esac

umask 077
pg_dump --dbname="$FAIRY_DATABASE_URL" --format=custom --no-owner --no-acl --file="$1"
pg_restore --list "$1" >/dev/null
echo "PostgreSQL backup verified: $1"
