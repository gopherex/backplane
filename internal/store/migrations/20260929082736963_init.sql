-- sqld:up
-- The store creates the schema before migrating (sqld_migrations lives in
-- it, through search_path): IF NOT EXISTS, and the down leaves it.
CREATE SCHEMA IF NOT EXISTS "backplane";
CREATE TABLE "backplane"."installation" (
  "id" uuid NOT NULL DEFAULT gen_random_uuid(),
  "singleton" bool NOT NULL DEFAULT true,
  "created_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("id")
);
ALTER TABLE "backplane"."installation" ADD CONSTRAINT "installation_singleton_check" CHECK (singleton);
ALTER TABLE "backplane"."installation" ADD CONSTRAINT "installation_singleton_key" UNIQUE ("singleton");

-- sqld:down
ALTER TABLE "backplane"."installation" DROP CONSTRAINT "installation_singleton_key";
ALTER TABLE "backplane"."installation" DROP CONSTRAINT "installation_singleton_check";
DROP TABLE "backplane"."installation";
