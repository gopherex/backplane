import { useCallback, useMemo } from 'react';
import { create } from '@bufbuild/protobuf';
import * as api from '@gopherex/backplane-api';
import { useClient, useSnapshotWatch } from '@gopherex/backplane-react';
import { usePlatformQuery } from '../runtime.js';
import { dyn, shapeOf, type Shape } from './shape.js';

/** Manifest contracts as the wiring catalog carries them. */
export type Hook = api.WiringContract['hooks'][number];
export type Activity = api.WiringContract['activities'][number];
export type Event = api.WiringContract['events'][number];

/** One contract of the catalog by its full name ("<service>.<Name>"). */
export interface Contract<T> { full: string; service: string; name: string; value: T }

/** The wiring catalog indexed for the editor: every hook, activity and event by full name. */
export interface WiringIndex {
  services: api.WiringContract[];
  hooks: Map<string, Contract<Hook>>;
  activities: Map<string, Contract<Activity>>;
  events: Map<string, Contract<Event>>;
}

export const emptyIndex: WiringIndex = { services: [], hooks: new Map(), activities: new Map(), events: new Map() };

export function indexCatalog(services: readonly api.WiringContract[]): WiringIndex {
  const index: WiringIndex = { services: [...services], hooks: new Map(), activities: new Map(), events: new Map() };
  for (const contract of services) {
    const add = <T extends { name: string }>(into: Map<string, Contract<T>>, list: readonly T[]) => {
      for (const value of list) into.set(`${contract.service}.${value.name}`, { full: `${contract.service}.${value.name}`, service: contract.service, name: value.name, value });
    };
    add(index.hooks, contract.hooks); add(index.activities, contract.activities); add(index.events, contract.events);
  }
  return index;
}

/**
 * The catalog, reloaded whenever the installation's catalog changes (a deploy
 * changes contracts, which is also when saved wiring may break).
 */
export function useWiringCatalog() {
  const client = useClient(api.WiringServiceClient), catalog = useClient(api.CatalogServiceClient);
  const watch = useSnapshotWatch(useCallback((signal: AbortSignal) => catalog.watchCatalog(create(api.WatchCatalogRequestSchema), { signal }), [catalog]));
  const stamp = watch.value?.services.map((service) => `${service.name}@${service.latestVersion}`).join(',') ?? '';
  const state = usePlatformQuery(`wiring-catalog:${stamp}`, (signal) => client.getWiringCatalog(create(api.GetWiringCatalogRequestSchema), { signal }));
  const index = useMemo(() => state.value ? indexCatalog(state.value.services) : emptyIndex, [state.value]);
  return { index, loading: state.loading && !state.value, error: state.error };
}

/** Shapes of what expressions read, from the catalog and the definition's steps. */
export interface Scope {
  /** Variables by name: req / event and meta, steps, and every step. */
  variables: Map<string, { shape: Shape; detail: string }>;
}

const metaShape: Shape = { kind: 'object', fields: ['id', 'source', 'subject', 'type', 'time'].map((name) => ({ name, shape: { kind: 'string' }, required: true })) };

/** The scope of a binding (hook) or rule (event) with its steps' activities. */
export function scopeOf(index: WiringIndex, source: { hook: string } | { event: string }, steps: Record<string, { activity: string } | undefined>, forRuleWhen = false): Scope {
  const variables: Scope['variables'] = new Map();
  if ('hook' in source) variables.set('req', { shape: shapeOf(index.hooks.get(source.hook)?.value.input), detail: `${source.hook} input` });
  else {
    variables.set('event', { shape: shapeOf(index.events.get(source.event)?.value.schema), detail: source.event });
    variables.set('meta', { shape: metaShape, detail: 'CloudEvents attributes' });
  }
  if (forRuleWhen) return { variables };
  const names = Object.keys(steps).sort();
  variables.set('steps', { shape: { kind: 'object', fields: names.map((name) => ({ name, shape: { kind: 'object', fields: [{ name: 'skipped', shape: { kind: 'bool' }, required: true }] }, required: true })) }, detail: 'step states' });
  for (const name of names) {
    const activity = steps[name]?.activity ?? '';
    variables.set(name, { shape: activity ? shapeOf(index.activities.get(activity)?.value.output) : dyn, detail: `${activity || 'step'} output` });
  }
  return { variables };
}
