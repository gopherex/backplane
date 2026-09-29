import { useCallback, useState } from 'react';
import { create, toJsonString } from '@bufbuild/protobuf';
import { CatalogServiceClient, GetServiceRequestSchema, GetServiceResponseSchema, WatchCatalogRequestSchema, type ServiceSummary } from '@gopherex/backplane-api';
import { useClient, useSnapshotWatch } from '@gopherex/backplane-react';
import { Button, DataTable, ResourceTree, type TreeNode } from '@gopherex/backplane-ui';
import { JSONViewer } from '@gopherex/backplane-editors';
import { ConnectionNotice, QueryState, usePlatformQuery } from './runtime.js';
import { usePlatformText } from './locales.js';

export function ServiceCatalog({ onSelect }: { onSelect: (service: ServiceSummary) => void }) {
  const client = useClient(CatalogServiceClient), text = usePlatformText();
  const state = useSnapshotWatch(useCallback((signal: AbortSignal) => client.watchCatalog(create(WatchCatalogRequestSchema), { signal }), [client]));
  return <section><ConnectionNotice />{state.status !== 'ready' && <p role="status">{text(state.status)}</p>}
    <DataTable label={text('service')} data={state.value?.services ?? []} getRowId={(service) => service.name} onActivate={onSelect} columns={[
      { id: 'name', label: text('service'), value: (service) => service.name }, { id: 'version', label: text('version'), value: (service) => service.latestVersion },
      { id: 'instances', label: text('instances'), value: (service) => service.instances }, { id: 'healthy', label: text('healthy'), value: (service) => service.healthy },
    ]} />
  </section>;
}
export function ServiceInspector({ service, mode }: { service: string; mode: 'dark' | 'light' }) {
  const client = useClient(CatalogServiceClient), text = usePlatformText(), [selected, setSelected] = useState<string>();
  const state = usePlatformQuery(`service:${service}`, (signal) => client.getService(create(GetServiceRequestSchema, { name: service }), { signal }));
  const instance = state.value?.instances.find((instance) => instance.id === selected);
  const nodes = state.value?.latest?.nodes ?? [];
  type MutableNode = { id: string; label: string; children: MutableNode[] };
  const tree: MutableNode[] = nodes.map((node) => ({ id: node.path, label: node.path, children: [] }));
  // Manifest paths are already depth-first; missing ancestors remain visible roots.
  const byPath = new Map(tree.map((node) => [node.id, node])); const roots = tree.filter((node) => {
    const slash = node.id.lastIndexOf('/'), parent = slash < 0 ? undefined : byPath.get(node.id.slice(0, slash));
    if (!parent) return true; parent.children!.push(node); return false;
  });
  return <QueryState state={state}><section aria-label={service} style={{ display: 'grid', gap: 12 }}>
    <Button type="button" variant="outline" onClick={state.refresh}>{text('refresh')}</Button>
    <DataTable label={text('instances')} data={state.value?.instances ?? []} getRowId={(instance) => instance.id} onActivate={(instance) => setSelected(instance.id)} columns={[
      { id: 'id', label: text('instance'), value: (instance) => instance.id, width: 280 }, { id: 'version', label: text('version'), value: (instance) => instance.state?.version },
      { id: 'ready', label: text('healthy'), value: (instance) => instance.healthy },
      { id: 'reason', label: text('reason'), value: (instance) => instance.state?.nodes.filter((node) => !node.ready).map((node) => `${node.path}: ${node.error}`).join('; '), width: 400 },
    ]} />
    <ResourceTree label={text('nodes')} nodes={roots} selected={[]} onSelectionChange={() => {}} height={220} />
    {instance?.state && <><DataTable label={text('readiness')} data={instance.state.nodes} getRowId={(node) => node.path} columns={[
      { id: 'path', label: text('node'), value: (node) => node.path }, { id: 'ready', label: text('ready'), value: (node) => node.ready }, { id: 'reason', label: text('reason'), value: (node) => node.error, width: 500 },
    ]} /><JSONViewer label={text('effective')} value={new TextDecoder().decode(instance.state.config)} mode={mode} />
      <DataTable label={text('source')} data={Object.entries(instance.state.sources).map(([path, source]) => ({ path, source }))} getRowId={(row) => row.path} columns={[{ id: 'path', label: text('key'), value: (row) => row.path }, { id: 'source', label: text('source'), value: (row) => row.source }]} />
    </>}
    {state.value && <details><summary>{text('manifest')}</summary><JSONViewer label={text('manifest')} value={toJsonString(GetServiceResponseSchema, state.value, { prettySpaces: 2 })} mode={mode} /></details>}
  </section></QueryState>;
}
