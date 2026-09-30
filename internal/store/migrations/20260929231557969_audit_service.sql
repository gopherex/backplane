-- sqld:up
ALTER TABLE "backplane"."audit_entry" ADD COLUMN "service" text NOT NULL DEFAULT ''::text;
CREATE INDEX "audit_entry_service_idx" ON "backplane"."audit_entry" ("service", "sequence");
-- Existing entries: the same attribution new writes compute in Go.
UPDATE "backplane"."audit_entry" SET "service" = "subject" WHERE "action" LIKE 'config.%';
UPDATE "backplane"."audit_entry" SET "service" = split_part("subject", '.', 1)
  WHERE "action" IN ('binding.save', 'binding.delete', 'binding.rollback');
UPDATE "backplane"."audit_entry" e SET "service" = split_part(v.definition->>'event', '.', 1)
  FROM (SELECT DISTINCT ON (rule_id) rule_id, definition FROM "backplane"."rule_version"
        WHERE definition IS NOT NULL ORDER BY rule_id, version DESC) v
  WHERE e."action" IN ('rule.save', 'rule.delete', 'rule.rollback', 'rule.pause') AND e."subject" = v.rule_id::text;
UPDATE "backplane"."audit_entry" SET "service" = COALESCE(
    substring("subject" from '(?:^|;)(?:service|subscriber)=([^;]+)'),
    split_part(substring("subject" from '(?:^|;)(?:hook|activity|event)=([^;]+)'), '.', 1),
    split_part(substring("subject" from '(?:^|;)workflow_id=(?:binding|test)/([^/;]+)'), '.', 1),
    '')
  WHERE "service" = '' AND "subject" ~ '(^|;)(service|subscriber|hook|activity|event|workflow_id)=';
UPDATE "backplane"."audit_entry" e SET "service" = split_part(v.definition->>'event', '.', 1)
  FROM (SELECT DISTINCT ON (rule_id) rule_id, definition FROM "backplane"."rule_version"
        WHERE definition IS NOT NULL ORDER BY rule_id, version DESC) v
  WHERE e."service" = '' AND e."action" IN ('rule.test', 'rule.cancel') AND e."subject" ~ ('(^|;)id=' || v.rule_id::text || '(;|$)');

-- sqld:down
DROP INDEX "backplane"."audit_entry_service_idx";
ALTER TABLE "backplane"."audit_entry" DROP COLUMN "service";
