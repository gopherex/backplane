-- sqld:up
ALTER TABLE "backplane"."console_admin" DROP CONSTRAINT "console_admin_singleton_check";
DROP TABLE "backplane"."console_admin";

-- sqld:down
CREATE TABLE "backplane"."console_admin" (
  "singleton" bool NOT NULL DEFAULT true,
  "token_hash" text NOT NULL,
  "updated_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("singleton")
);
ALTER TABLE "backplane"."console_admin" ADD CONSTRAINT "console_admin_singleton_check" CHECK (singleton);
