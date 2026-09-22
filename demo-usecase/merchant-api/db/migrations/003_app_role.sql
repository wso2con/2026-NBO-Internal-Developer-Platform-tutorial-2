-- The application role. merchant-api connects as this, never as the owner.
--
-- Constraint 6: the password arrives as a psql variable from the environment, never from
-- a file in the repository.
--
-- Note on style, same as mopay's 002: psql does NOT substitute :'vars' inside
-- dollar-quoted blocks, so the values are handed to PL/pgSQL through
-- set_config/current_setting instead.

\set ON_ERROR_STOP on

-- Output suppressed: set_config echoes its argument, and the app password must not reach
-- the migration logs.
\o /dev/null
SELECT set_config('merchant.app_role', :'app_role', false);
SELECT set_config('merchant.app_password', :'app_password', false);
\o

DO $$
DECLARE
    r text := current_setting('merchant.app_role');
    p text := current_setting('merchant.app_password');
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = r) THEN
        EXECUTE format('CREATE ROLE %I LOGIN PASSWORD %L', r, p);
    ELSE
        EXECUTE format('ALTER ROLE %I LOGIN PASSWORD %L', r, p);
    END IF;
END
$$;

GRANT CONNECT ON DATABASE :"db_name" TO :"app_role";
GRANT USAGE ON SCHEMA public TO :"app_role";

-- merchant-api creates and reads merchants. It is deliberately NOT granted DELETE: a
-- merchant with collections behind it cannot meaningfully cease to exist, and no API
-- operation offers deletion.
GRANT SELECT, INSERT, UPDATE ON merchants, fee_schedules TO :"app_role";
