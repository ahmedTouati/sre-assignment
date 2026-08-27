-- The API only reads authorization data, so its database role is read-only.
\getenv app_password POSTGRES_APP_PASSWORD
\getenv app_user POSTGRES_APP_USER
\getenv app_database POSTGRES_DB

BEGIN;

CREATE ROLE :"app_user" WITH
    LOGIN
    NOSUPERUSER
    NOCREATEDB
    NOCREATEROLE
    NOINHERIT
    NOREPLICATION
    NOBYPASSRLS
    PASSWORD :'app_password';

REVOKE ALL ON DATABASE :"app_database" FROM PUBLIC;
GRANT CONNECT ON DATABASE :"app_database" TO :"app_user";

REVOKE ALL ON SCHEMA public FROM PUBLIC;
REVOKE ALL ON ALL TABLES IN SCHEMA public FROM PUBLIC;
GRANT USAGE ON SCHEMA public TO :"app_user";
GRANT SELECT ON TABLE users, memberships, group_permissions TO :"app_user";

COMMIT;
