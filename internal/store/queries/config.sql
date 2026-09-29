-- Revisions of Live-value overrides (internal/config).

-- name: NextConfigRevision :one
SELECT (COALESCE(max(revision), 0) + 1)::bigint AS revision
FROM backplane.config_revision WHERE service = @service;

-- name: InsertConfigRevision :one
INSERT INTO backplane.config_revision (service, revision, overrides, kv, author, comment, rollback_of)
VALUES (@service, @revision, @overrides, @kv, @author, @comment, @rollback_of)
RETURNING service, revision, overrides, kv, author, comment, created_at, rollback_of;

-- name: SetConfigCurrent :exec
INSERT INTO backplane.config_current (service, revision) VALUES (@service, @revision)
ON CONFLICT (service) DO UPDATE SET revision = EXCLUDED.revision, updated_at = now();

-- name: GetConfigRevision :one
SELECT service, revision, overrides, kv, author, comment, created_at, rollback_of
FROM backplane.config_revision WHERE service = @service AND revision = @revision;

-- name: GetCurrentConfigRevision :one
SELECT r.service, r.revision, r.overrides, r.kv, r.author, r.comment, r.created_at, r.rollback_of
FROM backplane.config_current c
JOIN backplane.config_revision r ON r.service = c.service AND r.revision = c.revision
WHERE c.service = @service;

-- name: ListCurrentConfigKV :many
SELECT r.service, r.revision, r.kv
FROM backplane.config_current c
JOIN backplane.config_revision r ON r.service = c.service AND r.revision = c.revision
ORDER BY r.service;

-- name: ListConfigRevisions :many
SELECT service, revision, overrides, kv, author, comment, created_at, rollback_of
FROM backplane.config_revision
WHERE service = @service AND revision < @before
ORDER BY revision DESC
LIMIT @page_size;
