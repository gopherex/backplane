-- sqld:up
CREATE TABLE "backplane"."config_revision" (
  "service" text NOT NULL,
  "revision" bigint NOT NULL,
  "overrides" jsonb NOT NULL,
  "kv" jsonb NOT NULL,
  "author" text NOT NULL,
  "comment" text NOT NULL DEFAULT '',
  "created_at" timestamptz NOT NULL DEFAULT now(),
  "rollback_of" bigint NULL,
  PRIMARY KEY ("service", "revision")
);
ALTER TABLE "backplane"."config_revision" ADD CONSTRAINT "config_revision_revision_check" CHECK (revision > 0);
ALTER TABLE "backplane"."config_revision" ADD CONSTRAINT "config_revision_service_rollback_of_fkey" FOREIGN KEY ("service", "rollback_of") REFERENCES "backplane"."config_revision" ("service", "revision");
CREATE TABLE "backplane"."config_current" (
  "service" text NOT NULL,
  "revision" bigint NOT NULL,
  "updated_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("service")
);
ALTER TABLE "backplane"."config_current" ADD CONSTRAINT "config_current_service_revision_fkey" FOREIGN KEY ("service", "revision") REFERENCES "backplane"."config_revision" ("service", "revision");

-- sqld:down
ALTER TABLE "backplane"."config_current" DROP CONSTRAINT "config_current_service_revision_fkey";
DROP TABLE "backplane"."config_current";
ALTER TABLE "backplane"."config_revision" DROP CONSTRAINT "config_revision_service_rollback_of_fkey";
ALTER TABLE "backplane"."config_revision" DROP CONSTRAINT "config_revision_revision_check";
DROP TABLE "backplane"."config_revision";
