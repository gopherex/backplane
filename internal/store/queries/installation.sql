-- name: CreateInstallation :exec
INSERT INTO backplane.installation DEFAULT VALUES ON CONFLICT (singleton) DO NOTHING;

-- name: GetInstallation :one
SELECT id, created_at FROM backplane.installation;
