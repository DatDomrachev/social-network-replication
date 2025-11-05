#!/bin/bash
set -euo pipefail

sudo docker compose stop postgres-slave1

sudo docker compose stop postgres-slave2

sudo docker rm -f social-network-postgres-slave1-1 2>/dev/null || true

sudo docker rm -f social-network-postgres-slave2-1 2>/dev/null || true


sudo docker volume rm social-network_slave1_data

sudo docker volume rm social-network_slave2_data

sudo docker compose up -d --force-recreate postgres-master

sudo docker compose up -d --no-deps --force-recreate postgres-slave1

sudo docker compose up -d --no-deps --force-recreate postgres-slave2

sudo docker compose exec postgres-master psql -U user -d postgres -c "SELECT application_name, state, sync_state, client_addr FROM pg_stat_replication;"
