-- sqld:up
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
ALTER TABLE "backplane"."audit_clock" ADD CONSTRAINT "audit_clock_retained_after_check" CHECK (retained_after >= 0);
ALTER TABLE "backplane"."audit_clock" ADD CONSTRAINT "audit_clock_sequence_check" CHECK (sequence >= 0);
ALTER TABLE "backplane"."audit_clock" ADD CONSTRAINT "audit_clock_singleton_check" CHECK (singleton);
ALTER TABLE "backplane"."audit_entry" ADD CONSTRAINT "audit_entry_id_key" UNIQUE ("id");
ALTER TABLE "backplane"."audit_entry" ADD CONSTRAINT "audit_entry_sequence_check" CHECK (sequence > 0);
CREATE INDEX "audit_entry_created_at_idx" ON "backplane"."audit_entry" ("created_at");
CREATE INDEX "audit_entry_operation_id_idx" ON "backplane"."audit_entry" ("operation_id");
CREATE INDEX "audit_outbox_pending_idx" ON "backplane"."audit_outbox" ("available_at", "leased_until") WHERE (published_at IS NULL);
ALTER TABLE "backplane"."audit_outbox" ADD CONSTRAINT "audit_outbox_sequence_fkey" FOREIGN KEY ("sequence") REFERENCES "backplane"."audit_entry" ("sequence");

-- sqld:down
ALTER TABLE "backplane"."audit_outbox" DROP CONSTRAINT "audit_outbox_sequence_fkey";
DROP INDEX "backplane"."audit_outbox_pending_idx";
DROP INDEX "backplane"."audit_entry_operation_id_idx";
DROP INDEX "backplane"."audit_entry_created_at_idx";
ALTER TABLE "backplane"."audit_entry" DROP CONSTRAINT "audit_entry_sequence_check";
ALTER TABLE "backplane"."audit_entry" DROP CONSTRAINT "audit_entry_id_key";
ALTER TABLE "backplane"."audit_clock" DROP CONSTRAINT "audit_clock_singleton_check";
ALTER TABLE "backplane"."audit_clock" DROP CONSTRAINT "audit_clock_sequence_check";
ALTER TABLE "backplane"."audit_clock" DROP CONSTRAINT "audit_clock_retained_after_check";
DROP TABLE "backplane"."audit_outbox";
DROP TABLE "backplane"."audit_entry";
DROP TABLE "backplane"."audit_clock";
