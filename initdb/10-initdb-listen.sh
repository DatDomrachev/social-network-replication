#!/bin/bash
set -euo pipefail
PGDATA="${PGDATA:-/var/lib/postgresql/data}"

# слушать TCP
grep -qE "^[[:space:]]*listen_addresses" "$PGDATA/postgresql.conf" \
  || echo "listen_addresses = '*'" >> "$PGDATA/postgresql.conf"

grep -qE "^[[:space:]]*port[[:space:]]*=" "$PGDATA/postgresql.conf" \
  || echo "port = 5432" >> "$PGDATA/postgresql.conf"

# добавить правила в СТАНДАРТНЫЙ $PGDATA/pg_hba.conf
if ! grep -q "host[[:space:]]\+all[[:space:]]\+user[[:space:]]\+0\.0\.0\.0/0" "$PGDATA/pg_hba.conf"; then
  cat >> "$PGDATA/pg_hba.conf" <<'EOF'
host    all             user            0.0.0.0/0               md5
host    replication     user            0.0.0.0/0               md5
EOF
fi
