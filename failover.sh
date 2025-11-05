#!/bin/bash
set -euo pipefail

echo "=== Step 1: Stop old master ==="
sudo docker stop social-network-postgres-master-1

echo "=== Step 2: Promote slave2 to master ==="
sudo docker compose exec postgres-slave2 psql -U user -d postgres -c "SELECT pg_promote();"

echo "=== Step 3: Wait for promotion to complete ==="
for i in {1..30}; do
  if sudo docker compose exec postgres-slave2 psql -U user -d postgres -t -c "SELECT pg_is_in_recovery();" 2>/dev/null | grep -q "f"; then
    echo "✓ Promotion completed after $i seconds"
    break
  fi
  if [ $i -eq 30 ]; then
    echo "✗ Promotion timed out after 30 seconds"
    sudo docker compose logs postgres-slave2 --tail=50
    exit 1
  fi
  echo "  Waiting for promotion... ($i/30)"
  sleep 1
done

echo "=== Step 4: DISABLE synchronous replication first ==="
sudo docker compose exec postgres-slave2 psql -U user -d postgres -c "ALTER SYSTEM SET synchronous_commit = off;"
sudo docker compose exec postgres-slave2 psql -U user -d postgres -c "ALTER SYSTEM SET synchronous_standby_names = '';"
sudo docker compose exec postgres-slave2 psql -U user -d postgres -c "SELECT pg_reload_conf();"
echo "✓ Synchronous replication disabled"
sleep 2

echo "=== Step 5: Grant replication privileges ==="
sudo docker compose exec postgres-slave2 psql -U user -d postgres -c 'ALTER ROLE "user" WITH REPLICATION;'
sudo docker compose exec postgres-slave2 psql -U user -d postgres -c '\du'
echo "✓ Replication privileges granted"

echo "=== Step 6: Update pg_hba.conf ==="
sudo docker compose exec postgres-slave2 bash -c '
echo "host    replication     user            0.0.0.0/0               md5" > $PGDATA/pg_hba.conf
echo "host    all             user            0.0.0.0/0               md5" >> $PGDATA/pg_hba.conf
echo "local   all             all                                     trust" >> $PGDATA/pg_hba.conf
echo "host    all             all             127.0.0.1/32            trust" >> $PGDATA/pg_hba.conf
echo "host    all             all             ::1/128                 trust" >> $PGDATA/pg_hba.conf
'

echo "=== Step 7: Reload pg_hba.conf ==="
sudo docker compose exec postgres-slave2 psql -U user -d postgres -c "SELECT pg_reload_conf();"

echo "=== Step 8: Stop and remove slave1 ==="
sudo docker stop social-network-postgres-slave1-1 || true
sudo docker rm -f social-network-postgres-slave1-1 || true
sudo docker volume rm social-network_slave1_data || true

echo "=== Step 9: Initialize slave1 from slave2 ==="

# Останавливаем и удаляем
sudo docker stop social-network-postgres-slave1-1 || true
sudo docker rm -f social-network-postgres-slave1-1 || true
sudo docker volume rm social-network_slave1_data || true

# Инициализируем с НОВЫМ entrypoint (не используя setup-replica.sh)
sudo docker compose run --no-deps --rm \
  --entrypoint bash \
  -e PGDATA=/var/lib/postgresql/data \
  -e PGPASSWORD=password \
  postgres-slave1 -c '
set -euxo pipefail

echo "=== Waiting for slave2 to be ready ==="
until pg_isready -h postgres-slave2 -p 5432 -U user -q; do
  echo "Waiting for slave2..."
  sleep 2
done

echo "=== Cleaning PGDATA ==="
rm -rf /var/lib/postgresql/data/*

echo "=== Testing connection to slave2 ==="
psql -h postgres-slave2 -p 5432 -U user -d postgres -c "SELECT version();"

echo "=== Running pg_basebackup from slave2 ==="
pg_basebackup -h postgres-slave2 -p 5432 -D /var/lib/postgresql/data -U user -X stream -R -v

echo "=== Configuring standby mode ==="
echo "primary_conninfo = '\''host=postgres-slave2 port=5432 user=user password=password application_name=slave1'\''" >> /var/lib/postgresql/data/postgresql.auto.conf
echo "hot_standby = on" >> /var/lib/postgresql/data/postgresql.auto.conf

echo "=== Verifying configuration ==="
cat /var/lib/postgresql/data/postgresql.auto.conf | grep -E "primary_conninfo|hot_standby"

echo "=== Setting permissions ==="
chown -R postgres:postgres /var/lib/postgresql/data
chmod 700 /var/lib/postgresql/data

echo "=== Initialization complete ==="
'


echo "=== Step 10: Start slave1 with DEFAULT entrypoint ==="
# Запускаем с оригинальным entrypoint от postgres образа, НЕ с setup-replica.sh
sudo docker compose run -d --no-deps --name social-network-postgres-slave1-1 \
  --entrypoint "docker-entrypoint.sh" \
  --service-ports \
  postgres-slave1 postgres

echo "=== Step 11: Wait for slave1 to start and connect ==="
for i in {1..30}; do
  if sudo docker compose exec postgres-slave2 psql -U user -d postgres -t -c "SELECT COUNT(*) FROM pg_stat_replication WHERE application_name='slave1';" 2>/dev/null | grep -q "1"; then
    echo "✓ Slave1 connected after $i seconds"
    break
  fi
  if [ $i -eq 30 ]; then
    echo "⚠ Slave1 didn't connect within 30 seconds, but continuing..."
    break
  fi
  echo "  Waiting for slave1 to connect... ($i/30)"
  sleep 1
done

echo "=== Step 12: Enable synchronous replication ==="
sudo docker compose exec postgres-slave2 psql -U user -d postgres -c "ALTER SYSTEM SET synchronous_commit = on;"
sudo docker compose exec postgres-slave2 psql -U user -d postgres -c "ALTER SYSTEM SET synchronous_standby_names = 'FIRST 1 (slave1)';"
sudo docker compose exec postgres-slave2 psql -U user -d postgres -c "SELECT pg_reload_conf();"


echo "✓ Synchronous replication enabled"

echo "=== Step 13: Verify replication ==="
sudo docker compose exec postgres-slave2 psql -U user -d postgres -x -c "
SELECT
  application_name,
  state,
  sync_state,
  client_addr,
  replay_lag
FROM pg_stat_replication;"

echo "=== Step 14: Verify data consistency ==="
echo "Master (slave2) count:"
sudo docker compose exec postgres-slave2 psql -U user -d social_network -t -c "SELECT COUNT(*) FROM public.logs;"

echo "Slave1 count (may lag slightly):"
sleep 2
sudo docker compose exec postgres-slave1 psql -U user -d social_network -t -c "SELECT COUNT(*) FROM public.logs;"

echo ""
echo "=== Failover completed successfully! ==="
echo "New topology:"
echo "  Master: postgres-slave2 (port 5437)"
echo "  Slave:  postgres-slave1 (port 5436)"
