import { create, type JsonValue } from '@bufbuild/protobuf';
import { timestampFromMs, ValueSchema } from '@bufbuild/protobuf/wkt';
import { AuditConditionSchema, AuditField, AuditFilterSchema, AuditOperator, type AuditCondition, type AuditFilter, type AuditRecord } from '@gopherex/backplane-api';

/** A fixed field of an audit record, as the feed names it. */
export type AuditFieldName = 'source' | 'service' | 'action' | 'actor' | 'subject' | 'outcome' | 'operation' | 'severity' | 'trace_id';
export type AuditOp = 'is' | 'is_not' | 'contains' | 'not_contains' | 'prefix' | 'exists' | 'not_exists' | 'gt' | 'gte' | 'lt' | 'lte';
export type AuditTarget = { field: AuditFieldName } | { attribute: string };
/** One filter condition as the page keeps it (and the URL carries it). */
export interface AuditChip { target: AuditTarget; op: AuditOp; values: JsonValue[] }

export const auditFields: readonly AuditFieldName[] = ['source', 'service', 'action', 'actor', 'subject', 'outcome', 'operation', 'severity', 'trace_id'];
export const auditOps: readonly AuditOp[] = ['is', 'is_not', 'contains', 'not_contains', 'prefix', 'exists', 'not_exists', 'gt', 'gte', 'lt', 'lte'];
const fieldEnum: Record<AuditFieldName, AuditField> = {
  source: AuditField.SOURCE, service: AuditField.SERVICE, action: AuditField.ACTION, actor: AuditField.ACTOR, subject: AuditField.SUBJECT,
  outcome: AuditField.OUTCOME, operation: AuditField.OPERATION, severity: AuditField.SEVERITY, trace_id: AuditField.TRACE_ID,
};
const opEnum: Record<AuditOp, AuditOperator> = {
  is: AuditOperator.IS, is_not: AuditOperator.IS_NOT, contains: AuditOperator.CONTAINS, not_contains: AuditOperator.NOT_CONTAINS,
  prefix: AuditOperator.PREFIX, exists: AuditOperator.EXISTS, not_exists: AuditOperator.NOT_EXISTS, gt: AuditOperator.GT,
  gte: AuditOperator.GTE, lt: AuditOperator.LT, lte: AuditOperator.LTE,
};
/** Operators a fixed field takes: it is text. */
export const fieldOps: readonly AuditOp[] = ['is', 'is_not', 'contains', 'not_contains', 'prefix', 'exists', 'not_exists'];
export const takesValues = (op: AuditOp) => op !== 'exists' && op !== 'not_exists';
export const numeric = (op: AuditOp) => op === 'gt' || op === 'gte' || op === 'lt' || op === 'lte';

const valueOf = (value: JsonValue) => create(ValueSchema, value === null ? { kind: { case: 'nullValue', value: 0 } }
  : typeof value === 'boolean' ? { kind: { case: 'boolValue', value } } : typeof value === 'number' ? { kind: { case: 'numberValue', value } }
  : { kind: { case: 'stringValue', value: typeof value === 'string' ? value : JSON.stringify(value) } });

export function conditionOf(target: AuditTarget): AuditCondition {
  return create(AuditConditionSchema, { target: 'field' in target ? { case: 'field', value: fieldEnum[target.field] } : { case: 'attribute', value: target.attribute } });
}

/** The request filter of the page's chips, text and resolved range. */
export function filterOf(chips: readonly AuditChip[], text: string, range?: { from: number; to: number }): AuditFilter {
  return create(AuditFilterSchema, {
    text, start: range ? timestampFromMs(range.from) : undefined, end: range ? timestampFromMs(range.to) : undefined,
    conditions: chips.map((chip) => ({ ...conditionOf(chip.target), op: opEnum[chip.op], values: chip.values.map(valueOf) })),
  });
}

export const targetKey = (target: AuditTarget) => 'field' in target ? `field:${target.field}` : `attr:${target.attribute}`;
export const sameTarget = (a: AuditTarget, b: AuditTarget) => targetKey(a) === targetKey(b);

/** The chips with value added to target's is (or is_not) chip: the one-click filter. */
export function addValue(chips: readonly AuditChip[], target: AuditTarget, value: JsonValue, exclude = false): AuditChip[] {
  const op: AuditOp = exclude ? 'is_not' : 'is', at = chips.findIndex((chip) => sameTarget(chip.target, target) && chip.op === op);
  if (at < 0) return [...chips, { target, op, values: [value] }];
  const chip = chips[at]!;
  if (chip.values.some((v) => JSON.stringify(v) === JSON.stringify(value))) return [...chips];
  return chips.map((other, index) => index === at ? { ...chip, values: [...chip.values, value] } : other);
}

/** A typed value: JSON when it parses as a number, boolean or null, else the text. */
export function parseValue(text: string): JsonValue {
  const trimmed = text.trim();
  if (/^(-?\d+(\.\d+)?([eE][+-]?\d+)?|true|false|null)$/.test(trimmed)) return JSON.parse(trimmed) as JsonValue;
  return text;
}

/** Chips as a URL parameter and back; malformed input is dropped. */
export function encodeChips(chips: readonly AuditChip[]): string { return chips.length ? JSON.stringify(chips) : ''; }
export function decodeChips(raw: string | null): AuditChip[] {
  if (!raw) return [];
  try {
    const parsed: unknown = JSON.parse(raw);
    if (!Array.isArray(parsed)) return [];
    return parsed.filter((chip): chip is AuditChip => !!chip && typeof chip === 'object' && auditOps.includes((chip as AuditChip).op)
      && Array.isArray((chip as AuditChip).values) && !!(chip as AuditChip).target
      && (('field' in (chip as AuditChip).target && auditFields.includes((chip as { target: { field: AuditFieldName } }).target.field))
        || typeof (chip as { target: { attribute?: unknown } }).target.attribute === 'string'));
  } catch { return []; }
}

/** The latest first page merged over the shown records: new ones on top, the rest kept, newest first by time then id. */
export function mergeRecords(shown: readonly AuditRecord[], latest: readonly AuditRecord[]): AuditRecord[] {
  const byId = new Map(shown.map((record) => [record.id, record]));
  for (const record of latest) byId.set(record.id, record);
  const time = (record: AuditRecord) => record.time ? Number(record.time.seconds) * 1e3 + record.time.nanos / 1e6 : 0;
  return [...byId.values()].sort((a, b) => time(b) - time(a) || (a.id < b.id ? 1 : a.id > b.id ? -1 : 0));
}

/** An actor as people read it: console sessions and rules shortened. */
export function humanActor(actor: string): string {
  const [kind, id] = actor.includes(':') ? [actor.slice(0, actor.indexOf(':')), actor.slice(actor.indexOf(':') + 1)] : [actor, ''];
  if (kind === 'console' && id) return `operator · ${id.slice(0, 8)}`;
  if (kind === 'rule' && id) return `rule · ${id.slice(0, 8)}`;
  return actor;
}

/** Keys loggers add to every record (the SDK's xlog, OTel bridges): context, not facts. */
const logContext = new Set(['event.name', 'trace_id', 'span_id', 'service', 'instance', 'version', 'node', 'component', 'logger', 'caller']);

/** The facts worth a glance in the table's details column. */
export function detailFacts(record: AuditRecord): string[] {
  const attributes = (record.attributes ?? {}) as Record<string, JsonValue>, facts: string[] = [];
  if (record.message) facts.push(record.message);
  const show = (key: string, value: JsonValue | undefined, format = (v: JsonValue) => typeof v === 'string' ? v : JSON.stringify(v)) => {
    if (value !== undefined && value !== null && value !== '') facts.push(`${key} ${format(value)}`);
  };
  if (record.source === 1) { // platform: its detail
    show('rev', attributes.revision); show('rollback of', attributes.rollback_of); show('keys', attributes.keys, (v) => Array.isArray(v) ? v.join(', ') : String(v));
    show('workflow', attributes.workflow_id); show('signal', attributes.signal); show('reason', attributes.reason); show('note', attributes.note);
    show('code', attributes.code); show('affected', attributes.affected); if (attributes.paused !== undefined) facts.push(attributes.paused ? 'paused' : 'resumed');
    return facts;
  }
  for (const [key, value] of Object.entries(attributes)) {
    if (facts.length >= 3) break;
    if (key.startsWith('backplane.') || logContext.has(key)) continue;
    show(key, value);
  }
  return facts;
}

export const outcomeTone = (outcome: string) => outcome === 'succeeded' ? 'success' as const : outcome === 'failed' || outcome === 'rejected' ? 'danger' as const
  : outcome === 'partial' ? 'warning' as const : outcome === 'intent' ? 'info' as const : 'neutral' as const;
