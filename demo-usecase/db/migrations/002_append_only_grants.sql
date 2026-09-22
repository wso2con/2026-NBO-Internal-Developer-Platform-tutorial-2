-- Hard constraint 1 (README.md) / C-3.3 / NFR-6.
--
-- `ledger_entries` and `settlement_lines` are append-only. Not "an API that refuses" and
-- not a soft delete: the application role is not granted UPDATE or DELETE on them at all,
-- so an attempt is refused by Postgres regardless of what the application code does.
--
-- Migrations run as the owner role. Services connect as :app_role.
--
-- Note on style: psql does NOT substitute :'vars' inside dollar-quoted blocks, so the
-- values are handed to PL/pgSQL through set_config/current_setting instead.

\set ON_ERROR_STOP on

-- Output suppressed: set_config echoes its argument, and the app password must not
-- reach the migration logs.
\o /dev/null
SELECT set_config('mopay.app_role', :'app_role', false);
SELECT set_config('mopay.app_password', :'app_password', false);
\o

DO $$
DECLARE
    r text := current_setting('mopay.app_role');
    p text := current_setting('mopay.app_password');
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = r) THEN
        EXECUTE format('CREATE ROLE %I LOGIN PASSWORD %L', r, p);
    ELSE
        EXECUTE format('ALTER ROLE %I LOGIN PASSWORD %L', r, p);
    END IF;
END
$$;

GRANT CONNECT ON DATABASE :"db_name" TO :"app_role";
GRANT USAGE  ON SCHEMA public       TO :"app_role";

-- Default: the app may read and write everything.
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO :"app_role";
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO :"app_role";

-- Then take it back on the two append-only tables. This is the constraint.
REVOKE UPDATE, DELETE, TRUNCATE ON ledger_entries   FROM :"app_role";
REVOKE UPDATE, DELETE, TRUNCATE ON settlement_lines FROM :"app_role";

-- Make sure a future table created by the owner does not silently re-grant them.
ALTER DEFAULT PRIVILEGES IN SCHEMA public
    GRANT SELECT, INSERT ON TABLES TO :"app_role";

-- Verify, so a broken grant fails the migration rather than the demo.
DO $$
DECLARE
    r    text := current_setting('mopay.app_role');
    tbl  text;
    priv text;
BEGIN
    FOREACH tbl IN ARRAY ARRAY['ledger_entries','settlement_lines'] LOOP
        FOREACH priv IN ARRAY ARRAY['UPDATE','DELETE'] LOOP
            IF has_table_privilege(r, tbl, priv) THEN
                RAISE EXCEPTION
                    'append-only enforcement failed: role % still holds % on % (README hard constraint 1)',
                    r, priv, tbl;
            END IF;
        END LOOP;
        IF NOT has_table_privilege(r, tbl, 'INSERT') THEN
            RAISE EXCEPTION
                'role % lacks INSERT on % - the append path would be broken', r, tbl;
        END IF;
    END LOOP;
    RAISE NOTICE 'append-only enforcement verified on ledger_entries and settlement_lines for role %', r;
END
$$;
