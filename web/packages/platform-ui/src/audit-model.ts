import type { AuditEntry } from '@gopherex/backplane-api';
export function mergeAudit(previous: readonly AuditEntry[], incoming: readonly AuditEntry[], limit: number) {
  const entries = new Map(previous.map((entry) => [entry.id, entry])); incoming.forEach((entry) => entries.set(entry.id, entry));
  return [...entries.values()].sort((a, b) => a.sequence === b.sequence ? 0 : a.sequence > b.sequence ? -1 : 1).slice(0, limit);
}
