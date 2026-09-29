import { useState } from 'react';
import { create, toJsonString } from '@bufbuild/protobuf';
import * as api from '@gopherex/backplane-api';
import { useClient } from '@gopherex/backplane-react';
import { Button, ConfirmAction, DataTable, Input } from '@gopherex/backplane-ui';
import { JSONViewer } from '@gopherex/backplane-editors';
import { MutationState, QueryState, usePlatformAction, usePlatformQuery } from './runtime.js';
import { usePlatformText } from './locales.js';

export function EventStreams(props: { service: string; mode: 'dark' | 'light' }) { return <ServiceEvents key={props.service} {...props} />; }
function ServiceEvents({ service, mode }: { service: string; mode: 'dark' | 'light' }) {
  const client = useClient(api.EventServiceClient), text = usePlatformText(), [sequence, setSequence] = useState(0n), [event, setEvent] = useState(''), [selected, setSelected] = useState<api.EventMessage>();
  const streams = usePlatformQuery(`events:${service}`, (signal) => client.listEvents(create(api.ListEventsRequestSchema, { service }), { signal }));
  const messages = usePlatformQuery(`messages:${service}:${event}:${sequence}`, (signal) => client.peekMessages(create(api.PeekMessagesRequestSchema, { service, event, startSeq: sequence, limit: 100 }), { signal }));
  return <section style={{ display: 'grid', gap: 12 }}><QueryState state={streams} />{streams.value?.natsError && <p role="status">{streams.value.natsError}</p>}
    <DataTable label={text('event')} data={streams.value?.events ?? []} getRowId={(event) => event.event} columns={[
      { id: 'name', label: text('event'), value: (event) => event.event }, { id: 'messages', label: text('sequence'), value: (event) => event.messages },
      { id: 'consumers', label: text('consumer'), value: (event) => event.subscribers.map((subscriber) => `${subscriber.durable}: ${subscriber.state?.numPending ?? '—'}`).join(', '), width: 500 },
    ]} onActivate={(event) => { setEvent(event.event); setSequence(0n); setSelected(undefined); }} />
    <QueryState state={messages}><DataTable label={text('input')} data={messages.value?.messages ?? []} getRowId={(message) => String(message.seq)} onActivate={setSelected} columns={[
      { id: 'seq', label: text('sequence'), value: (message) => message.seq }, { id: 'event', label: text('event'), value: (message) => message.event }, { id: 'data', label: text('input'), value: (message) => message.data, width: 500 },
    ]} /></QueryState><Button type="button" variant="outline" disabled={!messages.value?.nextSeq || messages.loading} onClick={() => setSequence(messages.value!.nextSeq)}>{text('more')}</Button>
    {selected && <JSONViewer label={text('detail')} value={toJsonString(api.EventMessageSchema, selected, { prettySpaces: 2 })} mode={mode} />}
  </section>;
}
export function DeadLettersPanel({ subscriber, consumer = '', mode }: { subscriber: string; consumer?: string; mode: 'dark' | 'light' }) {
  return <DeadLetters key={`${subscriber}:${consumer}`} subscriber={subscriber} initialConsumer={consumer} mode={mode} />;
}
function DeadLetters({ subscriber, initialConsumer, mode }: { subscriber: string; initialConsumer: string; mode: 'dark' | 'light' }) {
  const client = useClient(api.EventServiceClient), text = usePlatformText(), [consumer, setConsumer] = useState(initialConsumer), [sequence, setSequence] = useState(0n), [selected, setSelected] = useState<string[]>([]), [detail, setDetail] = useState<api.DeadLetter>();
  const action = usePlatformAction<string>(), [selectionEpoch, setSelectionEpoch] = useState(0);
  const state = usePlatformQuery(`dlq:${subscriber}:${consumer}:${sequence}`, (signal) => client.listDeadLetters(create(api.ListDeadLettersRequestSchema, { subscriber, consumer, startSeq: sequence, limit: 100 }), { signal }));
  return <QueryState state={state}><section style={{ display: 'grid', gap: 12 }}><Input aria-label={text('consumer')} value={consumer} disabled={action.pending} onChange={(event) => { setConsumer(event.target.value); setSelected([]); setDetail(undefined); setSequence(0n); }} />
    <MutationState action={action} /><DataTable key={`${subscriber}:${consumer}:${sequence}:${selectionEpoch}`} label={text('deadLetters')} data={(state.value?.deadLetters ?? []).filter((letter) => !!letter.message)} getRowId={(letter) => String(letter.message!.seq)} selectable onSelectionChange={setSelected} onActivate={setDetail} columns={[
      { id: 'sequence', label: text('sequence'), value: (letter) => letter.message?.seq }, { id: 'consumer', label: text('consumer'), value: (letter) => letter.consumer },
      { id: 'error', label: text('failure'), value: (letter) => letter.error, width: 500 },
    ]} />
    <Button type="button" variant="outline" disabled={!state.value?.nextSeq || state.loading || action.pending} onClick={() => { setSequence(state.value!.nextSeq); setSelected([]); }}>{text('more')}</Button>
    <div style={{ display: 'flex', gap: 8 }}>{(['redrive', 'purge'] as const).map((operation) => <ConfirmAction key={operation} trigger={text(operation)} title={text('confirmTitle')} description={selected.join(', ')} disabled={!selected.length || operation === 'redrive' && !consumer || action.pending || action.disabled} onConfirm={async (signal) => {
      if (!selected.length) throw new Error('No selected messages');
      const response = await action.run(async (abort) => {
        const options = { signal: AbortSignal.any([signal, abort]) }, request = { subscriber, consumer, seqs: selected.map(BigInt) };
        if (operation === 'redrive') return toJsonString(api.RedriveDeadLettersResponseSchema, await client.redriveDeadLetters(create(api.RedriveDeadLettersRequestSchema, request), options), { prettySpaces: 2 });
        return toJsonString(api.PurgeDeadLettersResponseSchema, await client.purgeDeadLetters(create(api.PurgeDeadLettersRequestSchema, request), options), { prettySpaces: 2 });
      });
      if (response === undefined) throw new Error('Dead-letter outcome is unavailable'); setSelected([]); setSelectionEpoch((value) => value + 1); setDetail(undefined); state.refresh();
    }} />)}</div>
    {action.value && <JSONViewer label={text('output')} value={action.value} mode={mode} />}{detail && <JSONViewer label={text('detail')} value={toJsonString(api.DeadLetterSchema, detail, { prettySpaces: 2 })} mode={mode} />}
  </section></QueryState>;
}
