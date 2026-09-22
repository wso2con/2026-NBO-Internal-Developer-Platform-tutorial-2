#!/bin/sh
# Applies db/migrations/*.sql in order, as the owner role.
# Re-runnable: every migration is written to be idempotent.
set -eu

: "${POSTGRES_HOST:?POSTGRES_HOST is required}"
: "${POSTGRES_DB:?POSTGRES_DB is required}"
: "${POSTGRES_USER:?POSTGRES_USER is required}"
: "${PGPASSWORD:?PGPASSWORD is required (owner password)}"
: "${APP_DB_USER:?APP_DB_USER is required}"
: "${APP_DB_PASSWORD:?APP_DB_PASSWORD is required}"
: "${DATA_REGION:?DATA_REGION is required (KE or NG)}"
: "${HOUSE_ACCOUNT_ID:=mopay_house}"

echo "migrate: waiting for postgres at ${POSTGRES_HOST}:${POSTGRES_PORT:-5432}"
until pg_isready -h "$POSTGRES_HOST" -p "${POSTGRES_PORT:-5432}" -U "$POSTGRES_USER" -q; do
    sleep 1
done

for f in /migrations/*.sql; do
    echo "migrate: applying $(basename "$f")"
    psql -h "$POSTGRES_HOST" -p "${POSTGRES_PORT:-5432}" -U "$POSTGRES_USER" -d "$POSTGRES_DB" \
         -v ON_ERROR_STOP=1 \
         -v app_role="$APP_DB_USER" \
         -v app_password="$APP_DB_PASSWORD" \
         -v db_name="$POSTGRES_DB" \
         -v region="$DATA_REGION" \
         -v house_id="$HOUSE_ACCOUNT_ID" \
         -f "$f"
done

echo "migrate: all migrations applied"
