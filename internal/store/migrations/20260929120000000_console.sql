-- sqld:up
CREATE TABLE "backplane"."console_admin" (
  "singleton" bool NOT NULL DEFAULT true,
  "token_hash" text NOT NULL,
  "updated_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("singleton")
);
ALTER TABLE "backplane"."console_admin" ADD CONSTRAINT "console_admin_singleton_check" CHECK (singleton);
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
ALTER TABLE "backplane"."console_session" ADD CONSTRAINT "console_session_token_hash_key" UNIQUE ("token_hash");
CREATE INDEX console_session_expires_at_idx ON backplane.console_session USING btree (expires_at);

-- sqld:down
DROP INDEX "backplane"."console_session_expires_at_idx";
ALTER TABLE "backplane"."console_session" DROP CONSTRAINT "console_session_token_hash_key";
DROP TABLE "backplane"."console_session";
ALTER TABLE "backplane"."console_admin" DROP CONSTRAINT "console_admin_singleton_check";
DROP TABLE "backplane"."console_admin";
