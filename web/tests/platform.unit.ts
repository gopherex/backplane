import { describe, expect, it } from 'vitest';
import { create } from '@bufbuild/protobuf';
import { DurationSchema, TimestampSchema } from '@bufbuild/protobuf/wkt';
import { AuditEntrySchema } from '@gopherex/backplane-api';
import { mergeAudit } from '../packages/platform-ui/src/audit-model';
import { nativeJSON } from '../packages/platform-ui/src/serialization';
import { tempoToSpans, parseTelemetryJSON, telemetryJSON, otlpId, decimalMillisToNanos } from '../packages/platform-ui/src/telemetry-model';

describe('platform data adapters', () => {
  it('deduplicates audit replay and sorts uint64 sequence values exactly', () => {
    const a = create(AuditEntrySchema, { id: 'a', sequence: 9007199254740993n }), b = create(AuditEntrySchema, { id: 'b', sequence: 9007199254740994n }), c = create(AuditEntrySchema, { id: 'c', sequence: 9007199254740995n });
    expect(mergeAudit([a, b], [b, c], 2).map((entry) => entry.id)).toEqual(['c', 'b']);
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
