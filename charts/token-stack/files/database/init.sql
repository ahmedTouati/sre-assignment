-- Schema
CREATE TABLE users (
    id VARCHAR(36) PRIMARY KEY,
    email VARCHAR(255) UNIQUE NOT NULL
);

CREATE TABLE groups (
    id VARCHAR(36) PRIMARY KEY,
    name VARCHAR(100) NOT NULL
);

CREATE TABLE memberships (
    user_id VARCHAR(36) REFERENCES users(id),
    group_id VARCHAR(36) REFERENCES groups(id),
    PRIMARY KEY (user_id, group_id)
);

CREATE TABLE group_permissions (
    group_id VARCHAR(36) REFERENCES groups(id),
    permission VARCHAR(100) NOT NULL
);

-- Seed data
INSERT INTO groups VALUES ('grp-1', 'researchers'), ('grp-2', 'admins');

INSERT INTO group_permissions VALUES
    ('grp-1', 'read:data'), ('grp-1', 'write:data'),
    ('grp-2', 'read:data'), ('grp-2', 'admin:users');

INSERT INTO users VALUES
    ('user-1', 'alice@example.com'),
    ('user-2', 'bob@example.com');

INSERT INTO memberships VALUES
    ('user-1', 'grp-1'),  -- alice is a researcher
    ('user-2', 'grp-2');  -- bob is an admin

