-- sqld:up
CREATE TABLE "backplane"."binding_version" (
  "hook" text NOT NULL,
  "version" bigint NOT NULL,
  "definition" jsonb NULL,
  "author" text NOT NULL,
  "comment" text NOT NULL DEFAULT '',
  "created_at" timestamptz NOT NULL DEFAULT now(),
  "rollback_of" bigint NULL,
  PRIMARY KEY ("hook", "version")
);
ALTER TABLE "backplane"."binding_version" ADD CONSTRAINT "binding_version_version_check" CHECK (version > 0);
ALTER TABLE "backplane"."binding_version" ADD CONSTRAINT "binding_version_hook_rollback_of_fkey" FOREIGN KEY ("hook", "rollback_of") REFERENCES "backplane"."binding_version" ("hook", "version");
CREATE TABLE "backplane"."binding_current" (
  "hook" text NOT NULL,
  "version" bigint NOT NULL,
  "updated_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("hook")
);
ALTER TABLE "backplane"."binding_current" ADD CONSTRAINT "binding_current_hook_version_fkey" FOREIGN KEY ("hook", "version") REFERENCES "backplane"."binding_version" ("hook", "version");
CREATE TABLE "backplane"."rule" (
  "id" uuid NOT NULL DEFAULT gen_random_uuid(),
  "paused" boolean NOT NULL DEFAULT false,
  "created_at" timestamptz NOT NULL DEFAULT now(),
  "updated_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("id")
);
CREATE TABLE "backplane"."rule_version" (
  "rule_id" uuid NOT NULL,
  "version" bigint NOT NULL,
  "name" text NOT NULL,
  "definition" jsonb NULL,
  "author" text NOT NULL,
  "comment" text NOT NULL DEFAULT '',
  "created_at" timestamptz NOT NULL DEFAULT now(),
  "rollback_of" bigint NULL,
  PRIMARY KEY ("rule_id", "version")
);
ALTER TABLE "backplane"."rule_version" ADD CONSTRAINT "rule_version_version_check" CHECK (version > 0);
ALTER TABLE "backplane"."rule_version" ADD CONSTRAINT "rule_version_rule_id_fkey" FOREIGN KEY ("rule_id") REFERENCES "backplane"."rule" ("id");
ALTER TABLE "backplane"."rule_version" ADD CONSTRAINT "rule_version_rule_id_rollback_of_fkey" FOREIGN KEY ("rule_id", "rollback_of") REFERENCES "backplane"."rule_version" ("rule_id", "version");
CREATE TABLE "backplane"."rule_current" (
  "rule_id" uuid NOT NULL,
  "version" bigint NOT NULL,
  "updated_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("rule_id")
);
ALTER TABLE "backplane"."rule_current" ADD CONSTRAINT "rule_current_rule_id_fkey" FOREIGN KEY ("rule_id") REFERENCES "backplane"."rule" ("id");
ALTER TABLE "backplane"."rule_current" ADD CONSTRAINT "rule_current_rule_id_version_fkey" FOREIGN KEY ("rule_id", "version") REFERENCES "backplane"."rule_version" ("rule_id", "version");

-- sqld:down
ALTER TABLE "backplane"."rule_current" DROP CONSTRAINT "rule_current_rule_id_version_fkey";
ALTER TABLE "backplane"."rule_current" DROP CONSTRAINT "rule_current_rule_id_fkey";
DROP TABLE "backplane"."rule_current";
ALTER TABLE "backplane"."rule_version" DROP CONSTRAINT "rule_version_rule_id_rollback_of_fkey";
ALTER TABLE "backplane"."rule_version" DROP CONSTRAINT "rule_version_rule_id_fkey";
ALTER TABLE "backplane"."rule_version" DROP CONSTRAINT "rule_version_version_check";
DROP TABLE "backplane"."rule_version";
DROP TABLE "backplane"."rule";
ALTER TABLE "backplane"."binding_current" DROP CONSTRAINT "binding_current_hook_version_fkey";
DROP TABLE "backplane"."binding_current";
ALTER TABLE "backplane"."binding_version" DROP CONSTRAINT "binding_version_hook_rollback_of_fkey";
ALTER TABLE "backplane"."binding_version" DROP CONSTRAINT "binding_version_version_check";
DROP TABLE "backplane"."binding_version";
