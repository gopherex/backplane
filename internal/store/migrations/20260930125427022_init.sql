-- sqld:up
CREATE SCHEMA IF NOT EXISTS "backplane";
CREATE TABLE "backplane"."audit_clock" (
  "singleton" bool NOT NULL DEFAULT true,
  "sequence" int8 NOT NULL DEFAULT 0,
  "retained_after" int8 NOT NULL DEFAULT 0,
  PRIMARY KEY ("singleton")
);
CREATE TABLE "backplane"."audit_entry" (
  "sequence" int8 NOT NULL,
  "id" uuid NOT NULL,
  "created_at" timestamptz NOT NULL DEFAULT clock_timestamp(),
  "actor" text NOT NULL,
  "action" text NOT NULL,
  "subject" text NOT NULL,
  "outcome" text NOT NULL,
  "operation_id" uuid NOT NULL,
  "detail" jsonb NOT NULL DEFAULT '{}'::jsonb,
  "service" text NOT NULL DEFAULT ''::text,
  PRIMARY KEY ("sequence")
);
CREATE TABLE "backplane"."audit_outbox" (
  "sequence" int8 NOT NULL,
  "available_at" timestamptz NOT NULL DEFAULT now(),
  "lease" uuid,
  "leased_until" timestamptz NOT NULL DEFAULT '-infinity'::timestamp with time zone,
  "attempts" int8 NOT NULL DEFAULT 0,
  "published_at" timestamptz,
  PRIMARY KEY ("sequence")
);
CREATE TABLE "backplane"."binding_current" (
  "hook" text NOT NULL,
  "version" int8 NOT NULL,
  "updated_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("hook")
);
CREATE TABLE "backplane"."binding_version" (
  "hook" text NOT NULL,
  "version" int8 NOT NULL,
  "definition" jsonb,
  "author" text NOT NULL,
  "comment" text NOT NULL DEFAULT ''::text,
  "created_at" timestamptz NOT NULL DEFAULT now(),
  "rollback_of" int8,
  PRIMARY KEY ("hook", "version")
);
CREATE TABLE "backplane"."config_current" (
  "service" text NOT NULL,
  "revision" int8 NOT NULL,
  "updated_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("service")
);
CREATE TABLE "backplane"."config_revision" (
  "service" text NOT NULL,
  "revision" int8 NOT NULL,
  "overrides" jsonb NOT NULL,
  "kv" jsonb NOT NULL,
  "author" text NOT NULL,
  "comment" text NOT NULL DEFAULT ''::text,
  "created_at" timestamptz NOT NULL DEFAULT now(),
  "rollback_of" int8,
  PRIMARY KEY ("service", "revision")
);
CREATE TABLE "backplane"."console_session" (
  "id" uuid NOT NULL DEFAULT gen_random_uuid(),
  "token_hash" bytea NOT NULL,
  "created_at" timestamptz NOT NULL,
  "expires_at" timestamptz NOT NULL,
  "last_seen_at" timestamptz NOT NULL,
  "address" text NOT NULL,
  "user_agent" text NOT NULL,
  PRIMARY KEY ("id")
);
CREATE TABLE "backplane"."installation" (
  "id" uuid NOT NULL DEFAULT gen_random_uuid(),
  "singleton" bool NOT NULL DEFAULT true,
  "created_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("id")
);
CREATE TABLE "backplane"."rule" (
  "id" uuid NOT NULL DEFAULT gen_random_uuid(),
  "paused" bool NOT NULL DEFAULT false,
  "created_at" timestamptz NOT NULL DEFAULT now(),
  "updated_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("id")
);
CREATE TABLE "backplane"."rule_current" (
  "rule_id" uuid NOT NULL,
  "version" int8 NOT NULL,
  "updated_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("rule_id")
);
CREATE TABLE "backplane"."rule_version" (
  "rule_id" uuid NOT NULL,
  "version" int8 NOT NULL,
  "name" text NOT NULL,
  "definition" jsonb,
  "author" text NOT NULL,
  "comment" text NOT NULL DEFAULT ''::text,
  "created_at" timestamptz NOT NULL DEFAULT now(),
  "rollback_of" int8,
  PRIMARY KEY ("rule_id", "version")
);
ALTER TABLE "backplane"."audit_clock" ADD CONSTRAINT "audit_clock_retained_after_check" CHECK (retained_after >= 0);
ALTER TABLE "backplane"."audit_clock" ADD CONSTRAINT "audit_clock_sequence_check" CHECK (sequence >= 0);
ALTER TABLE "backplane"."audit_clock" ADD CONSTRAINT "audit_clock_singleton_check" CHECK (singleton);
ALTER TABLE "backplane"."audit_entry" ADD CONSTRAINT "audit_entry_id_key" UNIQUE ("id");
ALTER TABLE "backplane"."audit_entry" ADD CONSTRAINT "audit_entry_sequence_check" CHECK (sequence > 0);
ALTER TABLE "backplane"."binding_version" ADD CONSTRAINT "binding_version_version_check" CHECK (version > 0);
ALTER TABLE "backplane"."config_revision" ADD CONSTRAINT "config_revision_revision_check" CHECK (revision > 0);
ALTER TABLE "backplane"."console_session" ADD CONSTRAINT "console_session_token_hash_key" UNIQUE ("token_hash");
ALTER TABLE "backplane"."installation" ADD CONSTRAINT "installation_singleton_check" CHECK (singleton);
ALTER TABLE "backplane"."installation" ADD CONSTRAINT "installation_singleton_key" UNIQUE ("singleton");
ALTER TABLE "backplane"."rule_version" ADD CONSTRAINT "rule_version_version_check" CHECK (version > 0);
CREATE INDEX "audit_entry_created_at_idx" ON "backplane"."audit_entry" ("created_at");
CREATE INDEX "audit_entry_operation_id_idx" ON "backplane"."audit_entry" ("operation_id");
CREATE INDEX "audit_entry_service_idx" ON "backplane"."audit_entry" ("service", "sequence");
CREATE INDEX "audit_outbox_pending_idx" ON "backplane"."audit_outbox" ("available_at", "leased_until") WHERE (published_at IS NULL);
CREATE INDEX "console_session_expires_at_idx" ON "backplane"."console_session" ("expires_at");
ALTER TABLE "backplane"."audit_outbox" ADD CONSTRAINT "audit_outbox_sequence_fkey" FOREIGN KEY ("sequence") REFERENCES "backplane"."audit_entry" ("sequence");
ALTER TABLE "backplane"."binding_current" ADD CONSTRAINT "binding_current_hook_version_fkey" FOREIGN KEY ("hook", "version") REFERENCES "backplane"."binding_version" ("hook", "version");
ALTER TABLE "backplane"."binding_version" ADD CONSTRAINT "binding_version_hook_rollback_of_fkey" FOREIGN KEY ("hook", "rollback_of") REFERENCES "backplane"."binding_version" ("hook", "version");
ALTER TABLE "backplane"."config_current" ADD CONSTRAINT "config_current_service_revision_fkey" FOREIGN KEY ("service", "revision") REFERENCES "backplane"."config_revision" ("service", "revision");
ALTER TABLE "backplane"."config_revision" ADD CONSTRAINT "config_revision_service_rollback_of_fkey" FOREIGN KEY ("service", "rollback_of") REFERENCES "backplane"."config_revision" ("service", "revision");
ALTER TABLE "backplane"."rule_current" ADD CONSTRAINT "rule_current_rule_id_fkey" FOREIGN KEY ("rule_id") REFERENCES "backplane"."rule" ("id");
ALTER TABLE "backplane"."rule_current" ADD CONSTRAINT "rule_current_rule_id_version_fkey" FOREIGN KEY ("rule_id", "version") REFERENCES "backplane"."rule_version" ("rule_id", "version");
ALTER TABLE "backplane"."rule_version" ADD CONSTRAINT "rule_version_rule_id_fkey" FOREIGN KEY ("rule_id") REFERENCES "backplane"."rule" ("id");
ALTER TABLE "backplane"."rule_version" ADD CONSTRAINT "rule_version_rule_id_rollback_of_fkey" FOREIGN KEY ("rule_id", "rollback_of") REFERENCES "backplane"."rule_version" ("rule_id", "version");

-- sqld:down
ALTER TABLE "backplane"."rule_version" DROP CONSTRAINT "rule_version_rule_id_rollback_of_fkey";
ALTER TABLE "backplane"."rule_version" DROP CONSTRAINT "rule_version_rule_id_fkey";
ALTER TABLE "backplane"."rule_current" DROP CONSTRAINT "rule_current_rule_id_version_fkey";
ALTER TABLE "backplane"."rule_current" DROP CONSTRAINT "rule_current_rule_id_fkey";
ALTER TABLE "backplane"."config_revision" DROP CONSTRAINT "config_revision_service_rollback_of_fkey";
ALTER TABLE "backplane"."config_current" DROP CONSTRAINT "config_current_service_revision_fkey";
ALTER TABLE "backplane"."binding_version" DROP CONSTRAINT "binding_version_hook_rollback_of_fkey";
ALTER TABLE "backplane"."binding_current" DROP CONSTRAINT "binding_current_hook_version_fkey";
ALTER TABLE "backplane"."audit_outbox" DROP CONSTRAINT "audit_outbox_sequence_fkey";
DROP INDEX "backplane"."console_session_expires_at_idx";
DROP INDEX "backplane"."audit_outbox_pending_idx";
DROP INDEX "backplane"."audit_entry_service_idx";
DROP INDEX "backplane"."audit_entry_operation_id_idx";
DROP INDEX "backplane"."audit_entry_created_at_idx";
ALTER TABLE "backplane"."rule_version" DROP CONSTRAINT "rule_version_version_check";
ALTER TABLE "backplane"."installation" DROP CONSTRAINT "installation_singleton_key";
ALTER TABLE "backplane"."installation" DROP CONSTRAINT "installation_singleton_check";
ALTER TABLE "backplane"."console_session" DROP CONSTRAINT "console_session_token_hash_key";
ALTER TABLE "backplane"."config_revision" DROP CONSTRAINT "config_revision_revision_check";
ALTER TABLE "backplane"."binding_version" DROP CONSTRAINT "binding_version_version_check";
ALTER TABLE "backplane"."audit_entry" DROP CONSTRAINT "audit_entry_sequence_check";
ALTER TABLE "backplane"."audit_entry" DROP CONSTRAINT "audit_entry_id_key";
ALTER TABLE "backplane"."audit_clock" DROP CONSTRAINT "audit_clock_singleton_check";
ALTER TABLE "backplane"."audit_clock" DROP CONSTRAINT "audit_clock_sequence_check";
ALTER TABLE "backplane"."audit_clock" DROP CONSTRAINT "audit_clock_retained_after_check";
DROP TABLE "backplane"."rule_version";
DROP TABLE "backplane"."rule_current";
DROP TABLE "backplane"."rule";
DROP TABLE "backplane"."installation";
DROP TABLE "backplane"."console_session";
DROP TABLE "backplane"."config_revision";
DROP TABLE "backplane"."config_current";
DROP TABLE "backplane"."binding_version";
DROP TABLE "backplane"."binding_current";
DROP TABLE "backplane"."audit_outbox";
DROP TABLE "backplane"."audit_entry";
DROP TABLE "backplane"."audit_clock";
