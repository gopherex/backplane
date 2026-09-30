-- The audit feed (backplane.audit_feed) under one filter, the same in every
-- query: optional parameters drop their condition when unset. Fields
-- compared by equality are typed arrays (indexed); attributes that must hold
-- one value each are one object (@>, GIN); every other condition is an
-- element of @conditions: {"field": column | "key": attribute, "op": ...,
-- "values": [json], "patterns": [LIKE pattern], "number": n}.

-- name: SearchAudit :many
SELECT f.source, f.id, f.time, f.received_at, f.service, f.actor, f.action, f.subject, f.outcome, f.message,
  f.operation, f.sequence, f.attributes, f.resource, f.severity, f.trace_id, f.span_id
FROM backplane.audit_feed f
WHERE f.time >= @start_at?::timestamptz AND f.time < @end_at?::timestamptz
  AND f.source = ANY(@sources?::text[])
  AND f.service = ANY(@services?::text[])
  AND f.action = ANY(@actions?::text[])
  AND f.actor = ANY(@actors?::text[])
  AND f.subject = ANY(@subjects?::text[])
  AND f.outcome = ANY(@outcomes?::text[])
  AND f.operation = ANY(@operations?::text[])
  AND f.severity = ANY(@severities?::text[])
  AND f.trace_id = ANY(@trace_ids?::text[])
  AND f.attributes @> @attributes_must?::jsonb
  AND (f.message ILIKE @text?::text OR f.action ILIKE @text?::text OR f.subject ILIKE @text?::text
    OR f.attributes::text ILIKE @text?::text)
  AND NOT EXISTS (
    SELECT 1 FROM jsonb_array_elements(@conditions?::jsonb) AS c,
      LATERAL (SELECT COALESCE(to_jsonb(f) -> (c->>'field'), f.attributes -> (c->>'key')) AS v) AS t
    WHERE NOT COALESCE(CASE c->>'op'
      WHEN 'is' THEN t.v IN (SELECT jsonb_array_elements(c->'values'))
      WHEN 'is_not' THEN t.v IS NULL OR t.v NOT IN (SELECT jsonb_array_elements(c->'values'))
      WHEN 'contains' THEN (t.v #>> '{}') ILIKE ANY (SELECT jsonb_array_elements_text(c->'patterns'))
      WHEN 'not_contains' THEN t.v IS NULL OR NOT (t.v #>> '{}') ILIKE ANY (SELECT jsonb_array_elements_text(c->'patterns'))
      WHEN 'prefix' THEN (t.v #>> '{}') LIKE ANY (SELECT jsonb_array_elements_text(c->'patterns'))
      WHEN 'exists' THEN t.v IS NOT NULL AND t.v <> '""'::jsonb
      WHEN 'not_exists' THEN t.v IS NULL OR t.v = '""'::jsonb
      WHEN 'gt' THEN jsonb_typeof(t.v) = 'number' AND t.v::numeric > (c->>'number')::numeric
      WHEN 'gte' THEN jsonb_typeof(t.v) = 'number' AND t.v::numeric >= (c->>'number')::numeric
      WHEN 'lt' THEN jsonb_typeof(t.v) = 'number' AND t.v::numeric < (c->>'number')::numeric
      WHEN 'lte' THEN jsonb_typeof(t.v) = 'number' AND t.v::numeric <= (c->>'number')::numeric
    END, false))
  AND (f.time, f.id) < (@before_time?::timestamptz, @before_id?::uuid)
ORDER BY f.time DESC, f.id DESC
LIMIT @page_size;

-- name: AuditFirstTime :one
SELECT min(f.time)::timestamptz AS first
FROM backplane.audit_feed f
WHERE f.time >= @start_at?::timestamptz AND f.time < @end_at?::timestamptz
  AND f.source = ANY(@sources?::text[])
  AND f.service = ANY(@services?::text[])
  AND f.action = ANY(@actions?::text[])
  AND f.actor = ANY(@actors?::text[])
  AND f.subject = ANY(@subjects?::text[])
  AND f.outcome = ANY(@outcomes?::text[])
  AND f.operation = ANY(@operations?::text[])
  AND f.severity = ANY(@severities?::text[])
  AND f.trace_id = ANY(@trace_ids?::text[])
  AND f.attributes @> @attributes_must?::jsonb
  AND (f.message ILIKE @text?::text OR f.action ILIKE @text?::text OR f.subject ILIKE @text?::text
    OR f.attributes::text ILIKE @text?::text)
  AND NOT EXISTS (
    SELECT 1 FROM jsonb_array_elements(@conditions?::jsonb) AS c,
      LATERAL (SELECT COALESCE(to_jsonb(f) -> (c->>'field'), f.attributes -> (c->>'key')) AS v) AS t
    WHERE NOT COALESCE(CASE c->>'op'
      WHEN 'is' THEN t.v IN (SELECT jsonb_array_elements(c->'values'))
      WHEN 'is_not' THEN t.v IS NULL OR t.v NOT IN (SELECT jsonb_array_elements(c->'values'))
      WHEN 'contains' THEN (t.v #>> '{}') ILIKE ANY (SELECT jsonb_array_elements_text(c->'patterns'))
      WHEN 'not_contains' THEN t.v IS NULL OR NOT (t.v #>> '{}') ILIKE ANY (SELECT jsonb_array_elements_text(c->'patterns'))
      WHEN 'prefix' THEN (t.v #>> '{}') LIKE ANY (SELECT jsonb_array_elements_text(c->'patterns'))
      WHEN 'exists' THEN t.v IS NOT NULL AND t.v <> '""'::jsonb
      WHEN 'not_exists' THEN t.v IS NULL OR t.v = '""'::jsonb
      WHEN 'gt' THEN jsonb_typeof(t.v) = 'number' AND t.v::numeric > (c->>'number')::numeric
      WHEN 'gte' THEN jsonb_typeof(t.v) = 'number' AND t.v::numeric >= (c->>'number')::numeric
      WHEN 'lt' THEN jsonb_typeof(t.v) = 'number' AND t.v::numeric < (c->>'number')::numeric
      WHEN 'lte' THEN jsonb_typeof(t.v) = 'number' AND t.v::numeric <= (c->>'number')::numeric
    END, false));

-- name: AuditHistogram :many
SELECT date_bin(@step_seconds::float8 * interval '1 second', f.time, @origin::timestamptz)::timestamptz AS bucket,
  count(*) FILTER (WHERE f.source = 'platform') AS platform,
  count(*) FILTER (WHERE f.source = 'application') AS application,
  count(*) FILTER (WHERE f.outcome IN ('failed', 'rejected', 'partial', 'unknown')) AS failed
FROM backplane.audit_feed f
WHERE f.time >= @start_at?::timestamptz AND f.time < @end_at?::timestamptz
  AND f.source = ANY(@sources?::text[])
  AND f.service = ANY(@services?::text[])
  AND f.action = ANY(@actions?::text[])
  AND f.actor = ANY(@actors?::text[])
  AND f.subject = ANY(@subjects?::text[])
  AND f.outcome = ANY(@outcomes?::text[])
  AND f.operation = ANY(@operations?::text[])
  AND f.severity = ANY(@severities?::text[])
  AND f.trace_id = ANY(@trace_ids?::text[])
  AND f.attributes @> @attributes_must?::jsonb
  AND (f.message ILIKE @text?::text OR f.action ILIKE @text?::text OR f.subject ILIKE @text?::text
    OR f.attributes::text ILIKE @text?::text)
  AND NOT EXISTS (
    SELECT 1 FROM jsonb_array_elements(@conditions?::jsonb) AS c,
      LATERAL (SELECT COALESCE(to_jsonb(f) -> (c->>'field'), f.attributes -> (c->>'key')) AS v) AS t
    WHERE NOT COALESCE(CASE c->>'op'
      WHEN 'is' THEN t.v IN (SELECT jsonb_array_elements(c->'values'))
      WHEN 'is_not' THEN t.v IS NULL OR t.v NOT IN (SELECT jsonb_array_elements(c->'values'))
      WHEN 'contains' THEN (t.v #>> '{}') ILIKE ANY (SELECT jsonb_array_elements_text(c->'patterns'))
      WHEN 'not_contains' THEN t.v IS NULL OR NOT (t.v #>> '{}') ILIKE ANY (SELECT jsonb_array_elements_text(c->'patterns'))
      WHEN 'prefix' THEN (t.v #>> '{}') LIKE ANY (SELECT jsonb_array_elements_text(c->'patterns'))
      WHEN 'exists' THEN t.v IS NOT NULL AND t.v <> '""'::jsonb
      WHEN 'not_exists' THEN t.v IS NULL OR t.v = '""'::jsonb
      WHEN 'gt' THEN jsonb_typeof(t.v) = 'number' AND t.v::numeric > (c->>'number')::numeric
      WHEN 'gte' THEN jsonb_typeof(t.v) = 'number' AND t.v::numeric >= (c->>'number')::numeric
      WHEN 'lt' THEN jsonb_typeof(t.v) = 'number' AND t.v::numeric < (c->>'number')::numeric
      WHEN 'lte' THEN jsonb_typeof(t.v) = 'number' AND t.v::numeric <= (c->>'number')::numeric
    END, false))
GROUP BY bucket
ORDER BY bucket;

-- name: AuditFields :many
SELECT k.key::text AS attribute, count(*) AS count
FROM backplane.audit_feed f, LATERAL jsonb_object_keys(f.attributes) AS k(key)
WHERE f.time >= @start_at?::timestamptz AND f.time < @end_at?::timestamptz
  AND f.source = ANY(@sources?::text[])
  AND f.service = ANY(@services?::text[])
  AND f.action = ANY(@actions?::text[])
  AND f.actor = ANY(@actors?::text[])
  AND f.subject = ANY(@subjects?::text[])
  AND f.outcome = ANY(@outcomes?::text[])
  AND f.operation = ANY(@operations?::text[])
  AND f.severity = ANY(@severities?::text[])
  AND f.trace_id = ANY(@trace_ids?::text[])
  AND f.attributes @> @attributes_must?::jsonb
  AND (f.message ILIKE @text?::text OR f.action ILIKE @text?::text OR f.subject ILIKE @text?::text
    OR f.attributes::text ILIKE @text?::text)
  AND NOT EXISTS (
    SELECT 1 FROM jsonb_array_elements(@conditions?::jsonb) AS c,
      LATERAL (SELECT COALESCE(to_jsonb(f) -> (c->>'field'), f.attributes -> (c->>'key')) AS v) AS t
    WHERE NOT COALESCE(CASE c->>'op'
      WHEN 'is' THEN t.v IN (SELECT jsonb_array_elements(c->'values'))
      WHEN 'is_not' THEN t.v IS NULL OR t.v NOT IN (SELECT jsonb_array_elements(c->'values'))
      WHEN 'contains' THEN (t.v #>> '{}') ILIKE ANY (SELECT jsonb_array_elements_text(c->'patterns'))
      WHEN 'not_contains' THEN t.v IS NULL OR NOT (t.v #>> '{}') ILIKE ANY (SELECT jsonb_array_elements_text(c->'patterns'))
      WHEN 'prefix' THEN (t.v #>> '{}') LIKE ANY (SELECT jsonb_array_elements_text(c->'patterns'))
      WHEN 'exists' THEN t.v IS NOT NULL AND t.v <> '""'::jsonb
      WHEN 'not_exists' THEN t.v IS NULL OR t.v = '""'::jsonb
      WHEN 'gt' THEN jsonb_typeof(t.v) = 'number' AND t.v::numeric > (c->>'number')::numeric
      WHEN 'gte' THEN jsonb_typeof(t.v) = 'number' AND t.v::numeric >= (c->>'number')::numeric
      WHEN 'lt' THEN jsonb_typeof(t.v) = 'number' AND t.v::numeric < (c->>'number')::numeric
      WHEN 'lte' THEN jsonb_typeof(t.v) = 'number' AND t.v::numeric <= (c->>'number')::numeric
    END, false))
GROUP BY k.key
ORDER BY count(*) DESC, k.key
LIMIT @limit_fields;

-- name: AuditFacet :many
SELECT t.value, count(*) AS count, sum(count(*)) OVER ()::bigint AS total
FROM backplane.audit_feed f,
  LATERAL (SELECT COALESCE(to_jsonb(f) -> @target_field?::text, f.attributes -> @target_key?::text)::jsonb AS value) AS t
WHERE f.time >= @start_at?::timestamptz AND f.time < @end_at?::timestamptz
  AND f.source = ANY(@sources?::text[])
  AND f.service = ANY(@services?::text[])
  AND f.action = ANY(@actions?::text[])
  AND f.actor = ANY(@actors?::text[])
  AND f.subject = ANY(@subjects?::text[])
  AND f.outcome = ANY(@outcomes?::text[])
  AND f.operation = ANY(@operations?::text[])
  AND f.severity = ANY(@severities?::text[])
  AND f.trace_id = ANY(@trace_ids?::text[])
  AND f.attributes @> @attributes_must?::jsonb
  AND (f.message ILIKE @text?::text OR f.action ILIKE @text?::text OR f.subject ILIKE @text?::text
    OR f.attributes::text ILIKE @text?::text)
  AND NOT EXISTS (
    SELECT 1 FROM jsonb_array_elements(@conditions?::jsonb) AS c,
      LATERAL (SELECT COALESCE(to_jsonb(f) -> (c->>'field'), f.attributes -> (c->>'key')) AS v) AS t
    WHERE NOT COALESCE(CASE c->>'op'
      WHEN 'is' THEN t.v IN (SELECT jsonb_array_elements(c->'values'))
      WHEN 'is_not' THEN t.v IS NULL OR t.v NOT IN (SELECT jsonb_array_elements(c->'values'))
      WHEN 'contains' THEN (t.v #>> '{}') ILIKE ANY (SELECT jsonb_array_elements_text(c->'patterns'))
      WHEN 'not_contains' THEN t.v IS NULL OR NOT (t.v #>> '{}') ILIKE ANY (SELECT jsonb_array_elements_text(c->'patterns'))
      WHEN 'prefix' THEN (t.v #>> '{}') LIKE ANY (SELECT jsonb_array_elements_text(c->'patterns'))
      WHEN 'exists' THEN t.v IS NOT NULL AND t.v <> '""'::jsonb
      WHEN 'not_exists' THEN t.v IS NULL OR t.v = '""'::jsonb
      WHEN 'gt' THEN jsonb_typeof(t.v) = 'number' AND t.v::numeric > (c->>'number')::numeric
      WHEN 'gte' THEN jsonb_typeof(t.v) = 'number' AND t.v::numeric >= (c->>'number')::numeric
      WHEN 'lt' THEN jsonb_typeof(t.v) = 'number' AND t.v::numeric < (c->>'number')::numeric
      WHEN 'lte' THEN jsonb_typeof(t.v) = 'number' AND t.v::numeric <= (c->>'number')::numeric
    END, false))
  AND t.value IS NOT NULL AND t.value <> '""'::jsonb
GROUP BY t.value
ORDER BY count(*) DESC, t.value
LIMIT @limit_values;
