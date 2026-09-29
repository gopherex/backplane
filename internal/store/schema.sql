-- Declarative schema of backplane: the desired state of schema `backplane`.
-- Edit this file, then `make db-migration name=<what>` writes the migration
-- that takes the history to it, and `make db` regenerates the queries.
-- Every object is schema-qualified.

CREATE SCHEMA backplane;

-- The installation this database belongs to: one row, created by the first
-- backplane to start; replicas and restarts find it.
CREATE TABLE backplane.installation (
  id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
  singleton  boolean     NOT NULL DEFAULT true UNIQUE CHECK (singleton),
  created_at timestamptz NOT NULL DEFAULT now()
);

-- Live-value overrides of a service (§5.2): one row per revision, append-only;
-- revisions count from 1 per service, a rollback is a new revision copying an
-- older one's values (rollback_of). overrides: Live path ("greeter.suffix")
-- -> JSON value, as saved; kv: Live path -> the exact string delivered to
-- Consul KV config/<service>/<path with '/'> (§5.3).
CREATE TABLE backplane.config_revision (
  service     text        NOT NULL,
  revision    bigint      NOT NULL CHECK (revision > 0),
  overrides   jsonb       NOT NULL,
  kv          jsonb       NOT NULL,
  author      text        NOT NULL,
  comment     text        NOT NULL DEFAULT '',
  created_at  timestamptz NOT NULL DEFAULT now(),
  rollback_of bigint,
  PRIMARY KEY (service, revision),
  FOREIGN KEY (service, rollback_of) REFERENCES backplane.config_revision (service, revision)
);

-- The revision of each service that Consul KV must hold: the latest one.
CREATE TABLE backplane.config_current (
  service    text        PRIMARY KEY,
  revision   bigint      NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (service, revision) REFERENCES backplane.config_revision (service, revision)
);

-- The console's admin token (§11.3): one row, the token's argon2id hash as
-- a PHC string. The first start writes it (BACKPLANE_ADMIN_TOKEN or a
-- generated one); a rotation from the console replaces it.
CREATE TABLE backplane.console_admin (
  singleton  boolean     PRIMARY KEY DEFAULT true CHECK (singleton),
  token_hash text        NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now()
);

-- Console sessions (§11.3). The cookie carries a random token; only its
-- SHA-256 is stored. id is the public handle (list, revoke).
CREATE TABLE backplane.console_session (
  id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
  token_hash   bytea       NOT NULL UNIQUE,
  created_at   timestamptz NOT NULL,
  expires_at   timestamptz NOT NULL,
  last_seen_at timestamptz NOT NULL,
  address      text        NOT NULL,
  user_agent   text        NOT NULL
);

CREATE INDEX console_session_expires_at_idx ON backplane.console_session (expires_at);
