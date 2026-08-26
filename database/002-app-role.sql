-- The API only reads authorization data, so its database role is read-only.
\getenv app_password POSTGRES_APP_PASSWORD

BEGIN;

CREATE ROLE tokenapp WITH
    LOGIN
    NOSUPERUSER
    NOCREATEDB
    NOCREATEROLE
    NOINHERIT
    NOREPLICATION
    NOBYPASSRLS
    PASSWORD :'app_password';

REVOKE ALL ON DATABASE tokendb FROM PUBLIC;
GRANT CONNECT ON DATABASE tokendb TO tokenapp;

REVOKE ALL ON SCHEMA public FROM PUBLIC;
REVOKE ALL ON ALL TABLES IN SCHEMA public FROM PUBLIC;
GRANT USAGE ON SCHEMA public TO tokenapp;
GRANT SELECT ON TABLE users, memberships, group_permissions TO tokenapp;

COMMIT;
