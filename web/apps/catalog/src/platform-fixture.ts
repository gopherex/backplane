import { create, fromJsonString, toJsonString } from '@bufbuild/protobuf';
import * as api from '@gopherex/backplane-api';
import { WsStatusError, type ClientConstructor, type ClientRuntime, type ClientState } from '@gopherex/backplane-client';
import { SchemaSchema } from '@gopherex/backplane-api/schemapb/schema_pb';

const schema = create(SchemaSchema, { id: { name: 'input' }, fields: [
  { name: 'name', title: 'Recipient', kind: { case: 'string', value: { default: 'World', minLen: 1n } } },
  { name: 'sequence', title: 'Sequence', kind: { case: 'uint64', value: { default: 18446744073709551615n } } },
] });
const source = { name: 'hello', latestVersion: '1.0.0', instances: 1, healthy: 1, health: api.ServiceHealth.HEALTHY };
const traceId = '1234567890abcdef1234567890abcdef';
const bindingDefinition = () => create(api.BindingDefinitionSchema, { hook: 'hello.Greet', steps: [{ name: 'format', activity: 'formatter.Format' }] });
const ruleDefinition = () => create(api.RuleDefinitionSchema, { event: 'hello.Greeted', steps: [{ name: 'record', activity: 'formatter.Record' }] });
// The fixture's text form of definitions is their protobuf JSON.
function parsed<S extends typeof api.BindingDefinitionSchema | typeof api.RuleDefinitionSchema>(schema: S, text: string) {
  try { return { definition: fromJsonString(schema, text), errors: [] }; } catch (error) { return { errors: [{ line: 1, column: 1, message: error instanceof Error ? error.message : 'Invalid definition' }] }; }
}
function untilAbort(signal?: AbortSignal): Promise<void> { return new Promise((resolve) => { if (!signal || signal.aborted) resolve(); else signal.addEventListener('abort', () => resolve(), { once: true }); }); }

/** Explicit catalog-only transport. Missing methods fail; it never contacts a platform. */
export class PlatformFixture implements ClientRuntime {
  private state: ClientState = { connection: 'connected', session: { id: 'fixture', created_at: '', expires_at: '', idle_timeout_seconds: 3600 }, connectionId: 1 };
  private listeners = new Set<() => void>();
  failWrites = false; gap = false; watchOpens = 0; cursors: string[] = []; writes = 0; lastInput = ''; active = 0;
  revision = create(api.RevisionSchema, { service: 'hello', revision: 1n, values: { 'greeter.suffix': '"!"' }, author: 'fixture', comment: 'Initial' });
  getSnapshot = () => this.state;
  subscribe = (listener: () => void) => { this.listeners.add(listener); return () => this.listeners.delete(listener); };
  reconnect() { this.state = { ...this.state, connectionId: this.state.connectionId + 1 }; this.listeners.forEach((listener) => listener()); }
  logout() { this.state = { connection: 'anonymous', session: null, connectionId: this.state.connectionId }; this.listeners.forEach((listener) => listener()); }
  client<T extends object>(_constructor: ClientConstructor<T>): T {
    const fixture = this;
    const config = () => create(api.ServiceConfigSchema, { service: 'hello', live: ['greeter.suffix'], current: fixture.revision, instances: [{ id: 'hello-1', version: '1.0.0', appliedRevision: fixture.revision.revision, live: [{ path: 'greeter.suffix', value: fixture.revision.values['greeter.suffix'] }] }] });
    const entry = (id: string, sequence: bigint) => create(api.AuditEntrySchema, { id, sequence, actor: 'operator', action: 'config.save', subject: 'hello', outcome: 'succeeded', operationId: 'op-1' });
    const snapshot = async function* <R>(value: R, signal?: AbortSignal) { fixture.active++; try { yield value; await untilAbort(signal); } finally { fixture.active--; } };
    const beforeWrite = () => { fixture.writes++; if (fixture.failWrites) throw new WsStatusError(14, 'Fixture outage'); };
    const methods = {
      async getService() { return create(api.GetServiceResponseSchema, { summary: source, latest: { service: 'hello', version: '1.0.0', hooks: [{ name: 'Greet', input: schema }], activities: [{ name: 'Format', input: schema }], events: [{ name: 'Greeted', schema }], workflows: [{ name: 'Welcome', input: schema }], nodes: [{ path: 'greeter' }, { path: 'greeter/database' }] }, instances: [{ id: 'hello-1', registered: true, healthy: false, state: { id: 'hello-1', version: '1.0.0', nodes: [{ path: 'greeter/database', ready: false, error: 'Waiting for database' }], config: new TextEncoder().encode('{"token":"***"}'), sources: { 'greeter.suffix': 4 } } }] }); },
      watchCatalog(_request: unknown, options: api.CallOptions) { return snapshot(create(api.WatchCatalogResponseSchema, { services: [source] }), options.signal); },
      watchConfig(_request: unknown, options: api.CallOptions) { return snapshot(create(api.WatchConfigResponseSchema, { config: config() }), options.signal); },
      async listRevisions() { return create(api.ListRevisionsResponseSchema, { revisions: [fixture.revision] }); },
      async validateOverride(request: api.ValidateOverrideRequest) { return create(api.ValidateOverrideResponseSchema, { violations: request.values['greeter.suffix'] === '"invalid"' ? [{ path: 'greeter.suffix', code: 'INVALID_VALUE', message: 'Invalid suffix' }] : [] }); },
      async saveRevision(request: api.SaveRevisionRequest) { beforeWrite(); fixture.revision = create(api.RevisionSchema, { service: request.service, revision: fixture.revision.revision + 1n, values: request.values, comment: request.comment }); return create(api.SaveRevisionResponseSchema, { revision: fixture.revision }); },
      async rollback() { beforeWrite(); return create(api.RollbackResponseSchema, { revision: fixture.revision }); },
      async listAudit(request: api.ListAuditRequest) { fixture.gap = false; return create(api.ListAuditResponseSchema, { entries: request.filter?.actor && request.filter.actor !== 'operator' ? [] : [entry('a', 9007199254740993n)], watchCursor: 'snapshot' }); },
      async *watchAudit(request: api.WatchAuditRequest, options: api.CallOptions) {
        fixture.active++; fixture.watchOpens++; fixture.cursors.push(request.afterCursor);
        try { if (fixture.gap) throw new WsStatusError(11, 'Expired cursor');
          yield create(api.WatchAuditResponseSchema, { entries: request.filter?.actor && request.filter.actor !== 'operator' ? [] : [entry('b', 9007199254740994n)], cursor: 'delta' });
          yield create(api.WatchAuditResponseSchema, { cursor: 'empty-advanced' }); await untilAbort(options.signal);
        } finally { fixture.active--; }
      },
      async getObsCapabilities() { return create(api.GetObsCapabilitiesResponseSchema, { signals: [{ signal: api.ObsSignal.LOGS, languages: [api.ObsLanguage.LOGSQL] }, { signal: api.ObsSignal.METRICS, languages: [api.ObsLanguage.METRICSQL, api.ObsLanguage.PROMQL] }, { signal: api.ObsSignal.TRACES, languages: [api.ObsLanguage.LOGSQL, api.ObsLanguage.TRACEQL], traceLookup: true }], maxLimit: 1000, maxPoints: 10000 }); },
      async listObsSources() { return create(api.ListObsSourcesResponseSchema, { sources: [{ resource: { 'service.name': 'hello' } }, { resource: { 'service.name': 'kratos' } }] }); },
      async listObsFields() { return create(api.ListObsFieldsResponseSchema, { fields: ['service.name', 'trace_id'] }); },
      async queryObs(request: api.QueryObsRequest) { if (request.signal === api.ObsSignal.METRICS) return create(api.QueryObsResponseSchema, { result: { case: 'timeSeries', value: { series: [{ labels: { service: 'hello' }, samples: [{ timestampSeconds: '1790683200.123456789', value: '18446744073709551615' }] }] } } });
        if (request.signal === api.ObsSignal.TRACES) return create(api.QueryObsResponseSchema, { result: { case: 'traces', value: { traces: [{ traceId, rootName: 'Greet', rootService: 'hello', startUnixNano: '1790683200123456789', durationMillis: '1.000001' }] } } });
        return create(api.QueryObsResponseSchema, { info: { partial: true, warnings: ['Fixture partial response'] }, result: { case: 'rows', value: { rows: [{ fields: { _time: '2026-09-29T12:00:00.123456789Z', _msg: 'Hello from storage', trace_id: traceId, sequence: '18446744073709551615' } }] } } }); },
      async getTrace() { return create(api.GetTraceResponseSchema, { traceId, tempoJson: new TextEncoder().encode('{"trace":{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"hello"}}]},"scopeSpans":[{"spans":[{"traceId":"EjRWeJCrze8SNFZ4kKvN7w==","spanId":"EjRWeJCrze8=","name":"Greet","startTimeUnixNano":1790683200123456789,"endTimeUnixNano":1790683200124456790}]}]}]},"extension":9007199254740993}') }); },
      async callHook(request: api.CallHookRequest) { beforeWrite(); fixture.lastInput = request.input; return create(api.CallHookResponseSchema, { result: { output: request.input, workflowId: 'hook/hello/Greet/fixture', runId: 'run-1' } }); },
      async runActivity(request: api.RunActivityRequest) { beforeWrite(); fixture.lastInput = request.input; return create(api.RunActivityResponseSchema, { result: { output: request.input } }); },
      async publishTestEvent(request: api.PublishTestEventRequest) { beforeWrite(); fixture.lastInput = request.payload; return create(api.PublishTestEventResponseSchema, { id: 'event-1', seq: 9007199254740993n }); },
      async startWorkflow(request: api.StartWorkflowRequest) { beforeWrite(); fixture.lastInput = request.input; return create(api.StartWorkflowResponseSchema, { workflowId: 'workflow-1', runId: 'run-1' }); },
      async getBinding(request: api.GetBindingRequest) { return create(api.GetBindingResponseSchema, { version: { hook: request.hook, version: 1n, definition: { hook: request.hook, steps: [{ name: 'format', activity: 'formatter.Format' }] } } }); },
      async validateBinding(request: api.ValidateBindingRequest) { return create(api.ValidateBindingResponseSchema, { violations: request.definition?.steps.length ? [] : [{ path: 'steps', message: 'At least one step is required' }] }); },
      async saveBinding(request: api.SaveBindingRequest) { beforeWrite(); return create(api.SaveBindingResponseSchema, { version: { version: 2n, hook: request.definition?.hook, definition: request.definition } }); },
      async getRule(request: api.GetRuleRequest) { return create(api.GetRuleResponseSchema, request.id ? { rule: { id: request.id, current: { ruleId: request.id, version: 1n, name: 'Greeted rule', definition: ruleDefinition() } } } : {}); },
      watchBindings(_request: unknown, options: api.CallOptions) { return snapshot(create(api.WatchBindingsResponseSchema, { bindings: [{ hook: 'hello.Greet', service: 'hello', declared: true, required: true, state: api.BindingState.BOUND, current: { hook: 'hello.Greet', version: 1n, definition: bindingDefinition() } }] }), options.signal); },
      watchRules(_request: unknown, options: api.CallOptions) { return snapshot(create(api.WatchRulesResponseSchema, { rules: [{ id: 'rule-1', current: { ruleId: 'rule-1', version: 1n, name: 'Greeted rule', definition: ruleDefinition() } }] }), options.signal); },
      async getStream() { return create(api.GetStreamResponseSchema, { service: 'hello', events: { name: 'BP_EVENTS_hello', exists: true, messages: 9007199254740993n, bytes: 2048n, consumers: 2, replicas: 1 }, deadLetters: { name: 'BP_DLQ_hello', exists: true, messages: 1n, bytes: 128n, consumers: 0, replicas: 1 }, deadLetterCounts: [{ consumer: 'audit', count: 1n }] }); },
      async cancelBindingRun() { beforeWrite(); return create(api.CancelBindingRunResponseSchema); },
      async cancelRuleRun() { beforeWrite(); return create(api.CancelRuleRunResponseSchema); },
      async getRuleRun() { return create(api.GetRuleRunResponseSchema, { run: { run: { workflowId: 'rule/rule-1/1', runId: 'run-3', status: api.RunStatus.COMPLETED } } }); },
      async revokeSession() { beforeWrite(); return create(api.RevokeSessionResponseSchema); },
      async listBindings() { return create(api.ListBindingsResponseSchema, { bindings: [{ hook: 'hello.Greet', service: 'hello', declared: true, required: true, state: api.BindingState.BOUND, current: { hook: 'hello.Greet', version: 1n, definition: bindingDefinition() } }] }); },
      async listRules() { return create(api.ListRulesResponseSchema, { rules: [{ id: 'rule-1', current: { ruleId: 'rule-1', version: 1n, name: 'Greeted rule', definition: ruleDefinition() } }] }); },
      async formatBinding(request: api.FormatBindingRequest) { return create(api.FormatBindingResponseSchema, { text: toJsonString(api.BindingDefinitionSchema, request.definition ?? create(api.BindingDefinitionSchema)) }); },
      async parseBinding(request: api.ParseBindingRequest) { return create(api.ParseBindingResponseSchema, parsed(api.BindingDefinitionSchema, request.text)); },
      async formatRule(request: api.FormatRuleRequest) { return create(api.FormatRuleResponseSchema, { text: toJsonString(api.RuleDefinitionSchema, request.definition ?? create(api.RuleDefinitionSchema)) }); },
      async parseRule(request: api.ParseRuleRequest) { return create(api.ParseRuleResponseSchema, parsed(api.RuleDefinitionSchema, request.text)); },
      async listBindingVersions() { return create(api.ListBindingVersionsResponseSchema, { versions: [{ hook: 'hello.Greet', version: 1n, author: 'fixture', comment: 'Initial', definition: bindingDefinition() }] }); },
      async listRuleVersions() { return create(api.ListRuleVersionsResponseSchema, { versions: [{ ruleId: 'rule-1', version: 1n, name: 'Greeted rule', author: 'fixture', definition: ruleDefinition() }] }); },
      async listBindingRuns() { return create(api.ListBindingRunsResponseSchema, { runs: [{ workflowId: 'binding/hello.Greet/1', runId: 'run-2', status: api.RunStatus.COMPLETED, historyLength: 11n }] }); },
      async listRuleRuns() { return create(api.ListRuleRunsResponseSchema); },
      async getBindingRun() { return create(api.GetBindingRunResponseSchema, { run: { run: { workflowId: 'binding/hello.Greet/1', runId: 'run-2', status: api.RunStatus.COMPLETED } }, hook: 'hello.Greet', version: 1n, steps: [{ step: 'format', activity: 'formatter.Format', status: api.StepRunStatus.COMPLETED, attempt: 1 }] }); },
      async testBinding(request: api.TestBindingRequest) { beforeWrite(); fixture.lastInput = request.input; return create(api.TestBindingResponseSchema, { result: { output: request.input } }); },
      async testRule(request: api.TestRuleRequest) { beforeWrite(); fixture.lastInput = request.event; return create(api.TestRuleResponseSchema, { matched: true }); },
      async deleteBinding() { beforeWrite(); return create(api.DeleteBindingResponseSchema); },
      async rollbackBinding() { beforeWrite(); return create(api.RollbackBindingResponseSchema); },
      async rollbackRule() { beforeWrite(); return create(api.RollbackRuleResponseSchema); },
      async deleteRule() { beforeWrite(); return create(api.DeleteRuleResponseSchema); },
      async pauseRule() { beforeWrite(); return create(api.PauseRuleResponseSchema); },
      async resumeRule() { beforeWrite(); return create(api.ResumeRuleResponseSchema); },
      async listWorkflows() { return create(api.ListWorkflowsResponseSchema, { workflows: [{ service: 'hello', name: 'Welcome', kind: api.WorkflowKind.WORKFLOW, description: 'Greets a new user', input: schema }] }); },
      async getObsSelectors() { return create(api.GetObsSelectorsResponseSchema, { sources: { selectors: [{ resource: { 'service.name': 'hello' } }] } }); },
      async listObsFieldValues() { return create(api.ListObsFieldValuesResponseSchema, { values: ['hello', 'kratos'] }); },
      async listSessions() { return create(api.ListSessionsResponseSchema, { sessions: [{ id: 'fixture', current: true, address: '127.0.0.1' }] }); },
      async validateRule() { return create(api.ValidateRuleResponseSchema); },
      async saveRule(request: api.SaveRuleRequest) { beforeWrite(); return create(api.SaveRuleResponseSchema, { version: { ruleId: request.id || 'rule-1', version: 2n, name: request.name, definition: request.definition } }); },
      async listRuns() { return create(api.ListRunsResponseSchema, { runs: [{ workflowId: 'welcome-1', runId: 'run-1', status: api.RunStatus.FAILED, historyLength: 9007199254740993n }] }); },
      async getRun() { return create(api.GetRunResponseSchema, { run: { workflowId: 'welcome-1', runId: 'run-1', status: api.RunStatus.RUNNING }, input: '{"sequence":18446744073709551615}', failure: 'Activity failed', failureType: 'DatabaseError', history: [{ id: 9007199254740993n, type: 'ActivityTaskFailed', failure: 'DatabaseError' }] }); },
      async cancelRun() { beforeWrite(); return create(api.CancelRunResponseSchema); },
      async terminateRun() { beforeWrite(); return create(api.TerminateRunResponseSchema); },
      async signalRun() { beforeWrite(); return create(api.SignalRunResponseSchema); },
      async listSchedules() { return create(api.ListSchedulesResponseSchema, { schedules: [{ service: 'hello', name: 'Welcome', id: 'hello/Welcome', state: { paused: true, actionCount: 9007199254740993n } }] }); },
      async pauseSchedule() { beforeWrite(); return create(api.PauseScheduleResponseSchema); },
      async unpauseSchedule() { beforeWrite(); return create(api.UnpauseScheduleResponseSchema); },
      async triggerSchedule() { beforeWrite(); return create(api.TriggerScheduleResponseSchema); },
      async listEvents() { return create(api.ListEventsResponseSchema, { events: [{ event: 'hello.Greeted', service: 'hello', name: 'Greeted', declared: true, messages: 9007199254740993n, lastSeq: 9007199254740993n, subscribers: [{ kind: api.SubscriberKind.REACTOR, service: 'hello', consumer: 'audit', durable: 'hello-audit', deadLetters: 1n, state: { numPending: 2n } }] }] }); },
      async peekMessages() { return create(api.PeekMessagesResponseSchema, { messages: [{ seq: 9007199254740993n, event: 'hello.Greeted', data: '{"name":"World"}' }] }); },
      async listDeadLetters() { return create(api.ListDeadLettersResponseSchema, { deadLetters: [{ consumer: 'audit', error: 'DatabaseError', delivered: 5, message: { seq: 9007199254740993n, event: 'hello.Greeted' } }], counts: [{ consumer: 'audit', count: 1n }] }); },
      async redriveDeadLetters(request: api.RedriveDeadLettersRequest) { beforeWrite(); fixture.lastInput = request.seqs.map(String).join(','); return create(api.RedriveDeadLettersResponseSchema, { redriven: 1n }); },
      async purgeDeadLetters(request: api.PurgeDeadLettersRequest) { beforeWrite(); fixture.lastInput = request.seqs.map(String).join(','); return create(api.PurgeDeadLettersResponseSchema, { purged: 1n }); },
    };
    return new Proxy(methods, { get(target, key) { if (key in target) return Reflect.get(target, key); throw new Error(`No explicit fixture for ${String(key)}`); } }) as unknown as T;
  }
}
