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

-- Bindings (§7.1): versions of the binding of each hook ("<service>.<Hook>"),
-- append-only; versions count from 1 per hook, a rollback is a new version
-- copying an older one's definition (rollback_of), a delete is a tombstone
-- (definition NULL). definition: protojson of backplane.console.v1.BindingDefinition.
CREATE TABLE backplane.binding_version (
  hook        text        NOT NULL,
  version     bigint      NOT NULL CHECK (version > 0),
  definition  jsonb,
  author      text        NOT NULL,
  comment     text        NOT NULL DEFAULT '',
  created_at  timestamptz NOT NULL DEFAULT now(),
  rollback_of bigint,
  PRIMARY KEY (hook, version),
  FOREIGN KEY (hook, rollback_of) REFERENCES backplane.binding_version (hook, version)
);

-- The version of each hook's binding in force: the latest one.
CREATE TABLE backplane.binding_current (
  hook       text        PRIMARY KEY,
  version    bigint      NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (hook, version) REFERENCES backplane.binding_version (hook, version)
);

-- Rules (§8.1): event -> steps. The row is the rule's identity and its
-- pause, which is not versioned.
CREATE TABLE backplane.rule (
  id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
  paused     boolean     NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

-- Versions of a rule, as binding_version: name and definition (protojson of
-- backplane.console.v1.RuleDefinition; NULL: the tombstone of a delete).
CREATE TABLE backplane.rule_version (
  rule_id     uuid        NOT NULL REFERENCES backplane.rule (id),
  version     bigint      NOT NULL CHECK (version > 0),
  name        text        NOT NULL,
  definition  jsonb,
  author      text        NOT NULL,
  comment     text        NOT NULL DEFAULT '',
  created_at  timestamptz NOT NULL DEFAULT now(),
  rollback_of bigint,
  PRIMARY KEY (rule_id, version),
  FOREIGN KEY (rule_id, rollback_of) REFERENCES backplane.rule_version (rule_id, version)
);

-- The version of each rule in force: the latest one.
CREATE TABLE backplane.rule_current (
  rule_id    uuid        PRIMARY KEY REFERENCES backplane.rule (id),
  version    bigint      NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (rule_id, version) REFERENCES backplane.rule_version (rule_id, version)
);
