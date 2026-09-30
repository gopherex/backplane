-- name: InsertApplicationAudit :execrows
INSERT INTO backplane.app_audit (id, key, time, service, action, actor, subject, outcome, severity, body,
  attributes, resource, trace_id, span_id)
VALUES (@id, @key, @time, @service, @action, @actor, @subject, @outcome, @severity, @body,
  @attributes, @resource, @trace_id, @span_id)
ON CONFLICT (key) DO NOTHING;
