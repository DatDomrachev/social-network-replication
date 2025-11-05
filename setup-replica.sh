#!/bin/bash
set -euo pipefail

PGDATA="${PGDATA:-/var/lib/postgresql/data}"
ROLE="${PGAPPNAME:-slave1}"
MASTER="postgres-master"
PORT="5432"
USER="user"
PASS="password"

echo "[replica] waiting for master ${MASTER}:${PORT} ..."
until pg_isready -h "$MASTER" -p "$PORT" -q; do sleep 1; done
echo "[replica] master is ready"

if [ -z "$(ls -A "$PGDATA" 2>/dev/null || true)" ]; then
  echo "[replica] basebackup ..."
  export PGPASSWORD="$PASS"
  pg_basebackup -h "$MASTER" -p "$PORT" -D "$PGDATA" -U "$USER" -X stream -R -v --progress
  # Жестко задаем primary_conninfo c application_name для sync_names
  echo "primary_conninfo = 'host=${MASTER} port=${PORT} user=${USER} password=${PASS} application_name=${ROLE}'" >> "$PGDATA/postgresql.auto.conf"
  echo "hot_standby = on" >> "$PGDATA/postgresql.auto.conf"
  chown -R postgres:postgres "$PGDATA"
  chmod 700 "$PGDATA"
else
  echo "[replica] PGDATA not empty, skip basebackup"
fi

exec docker-entrypoint.sh postgres
