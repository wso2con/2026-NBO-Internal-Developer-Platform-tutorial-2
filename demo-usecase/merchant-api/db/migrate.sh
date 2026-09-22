#!/bin/sh
# Applies merchant-api's migrations in order, as the owner role.
# Re-runnable: every migration is written to be idempotent.
#
# This is merchant-api's OWN datastore and its own migration path. It shares nothing with
# mopay's database - not an instance, not a role, not a password.
set -eu

: "${POSTGRES_HOST:?POSTGRES_HOST is required}"
: "${POSTGRES_DB:?POSTGRES_DB is required}"
: "${POSTGRES_USER:?POSTGRES_USER is required}"
: "${PGPASSWORD:?PGPASSWORD is required (owner password)}"
: "${APP_DB_USER:?APP_DB_USER is required}"
: "${APP_DB_PASSWORD:?APP_DB_PASSWORD is required}"

echo "merchant-migrate: waiting for postgres at ${POSTGRES_HOST}:${POSTGRES_PORT:-5432}"
until pg_isready -h "$POSTGRES_HOST" -p "${POSTGRES_PORT:-5432}" -U "$POSTGRES_USER" -q; do
    sleep 1
done

for f in /migrations/*.sql; do
    echo "merchant-migrate: applying $(basename "$f")"
    psql -h "$POSTGRES_HOST" -p "${POSTGRES_PORT:-5432}" -U "$POSTGRES_USER" -d "$POSTGRES_DB" \
         -v ON_ERROR_STOP=1 \
         -v app_role="$APP_DB_USER" \
         -v app_password="$APP_DB_PASSWORD" \
         -v db_name="$POSTGRES_DB" \
         -f "$f"
done

echo "merchant-migrate: all migrations applied"
