import { describe, expect, it } from 'vitest';
import { create } from '@bufbuild/protobuf';
import { DurationSchema, TimestampSchema } from '@bufbuild/protobuf/wkt';
import { AuditField, AuditOperator, AuditRecordSchema, AuditSource } from '@gopherex/backplane-api';
import { addValue, decodeChips, detailFacts, encodeChips, filterOf, humanActor, mergeRecords, parseValue } from '../packages/platform-ui/src/audit-model';
import { nativeJSON } from '../packages/platform-ui/src/serialization';
import { tempoToSpans, parseTelemetryJSON, telemetryJSON, otlpId, decimalMillisToNanos } from '../packages/platform-ui/src/telemetry-model';

describe('platform data adapters', () => {
  it('deduplicates audit replay and sorts uint64 sequence values exactly', () => {
    const at = (seconds: number) => ({ seconds: BigInt(seconds), nanos: 0 });
    const a = create(AuditRecordSchema, { id: 'a', time: at(1) }), b = create(AuditRecordSchema, { id: 'b', time: at(2) }), c = create(AuditRecordSchema, { id: 'c', time: at(3) });
    expect(mergeRecords([a, b], [c, b]).map((record) => record.id)).toEqual(['c', 'b', 'a']);
  });
  it('serializes schema values to application JSON with exact integer literals', () => {
    expect(nativeJSON({ count: 18446744073709551615n, timeout: create(DurationSchema, { seconds: 1n, nanos: 1 }), at: create(TimestampSchema, { seconds: 0n, nanos: 1 }), bytes: new Uint8Array([1, 2]) })).toBe('{"count":18446744073709551615,"timeout":"1.000000001s","at":"1970-01-01T00:00:00.000000001Z","bytes":"AQI="}');
  });
  it('keeps backend JSON extensions and converts OTLP IDs and nested attributes', () => {
    const raw = '{"trace":{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"hello"}}]},"scopeSpans":[{"spans":[{"traceId":"EjRWeJCrze8SNFZ4kKvN7w==","spanId":"EjRWeJCrze8=","name":"Greet","startTimeUnixNano":1790683200123456789,"endTimeUnixNano":"1790683200123456799","attributes":[{"key":"sequence","value":{"intValue":"18446744073709551615"}}],"status":{"code":2}}]}]}]},"extension":9007199254740993}';
    expect(telemetryJSON(parseTelemetryJSON(raw))).toContain('9007199254740993');
    const [span] = tempoToSpans(raw); expect(span.traceId).toBe('1234567890abcdef1234567890abcdef'); expect(span.id).toBe('1234567890abcdef');
    expect(span.startUnixNano).toBe('1790683200123456789'); expect(span.attributes?.sequence).toBe(18446744073709551615n); expect(span.status).toBe('error'); expect(span.service).toBe('hello');
    expect(otlpId('ABCDEF0123456789', 8)).toBe('abcdef0123456789'); expect(decimalMillisToNanos('1.000001')).toBe('1000001');
    expect(decimalMillisToNanos('1e-3')).toBe('1000'); expect(decimalMillisToNanos('1.123456789')).toBe('1123456');
    expect(decimalMillisToNanos('9007199254740993')).toBe('9007199254740993000000');
    expect(decimalMillisToNanos('NaN')).toBe(''); expect(decimalMillisToNanos('-1')).toBe('');
  });
});

describe('audit filters', () => {
  it('builds conditions, one-click values and the URL form', () => {
    let chips = addValue([], { field: 'service' }, 'iam');
    chips = addValue(chips, { field: 'service' }, 'billing');
    chips = addValue(chips, { attribute: 'tenant' }, 'acme', true);
    expect(chips).toEqual([{ target: { field: 'service' }, op: 'is', values: ['iam', 'billing'] }, { target: { attribute: 'tenant' }, op: 'is_not', values: ['acme'] }]);
    expect(decodeChips(encodeChips(chips))).toEqual(chips);
    expect(decodeChips('[{"target":{"field":"nope"},"op":"is","values":[]}]')).toEqual([]);
    expect(decodeChips('not json')).toEqual([]);
    const filter = filterOf(chips, 'text', { from: 1000, to: 2000 });
    expect(filter.conditions[0]!.target).toEqual({ case: 'field', value: AuditField.SERVICE });
    expect(filter.conditions[1]!.op).toBe(AuditOperator.IS_NOT);
    expect(filter.start?.seconds).toBe(1n);
    expect(parseValue('3')).toBe(3); expect(parseValue('true')).toBe(true); expect(parseValue('acme')).toBe('acme');
  });

  it('reads actors and details as people do', () => {
    expect(humanActor('console:1b7e2c9a-0000-4000')).toBe('operator · 1b7e2c9a');
    expect(humanActor('user:7')).toBe('user:7');
    const platform = create(AuditRecordSchema, { source: AuditSource.PLATFORM, attributes: { revision: 3, keys: ['a', 'b'], workflow_id: 'wf' } });
    expect(detailFacts(platform)).toEqual(['rev 3', 'keys a, b', 'workflow wf']);
    const application = create(AuditRecordSchema, { source: AuditSource.APPLICATION, message: 'deleted', attributes: { 'backplane.audit': true, 'event.name': 'x', tenant: 'acme' } });
    expect(detailFacts(application)).toEqual(['deleted', 'tenant acme']);
  });
});
