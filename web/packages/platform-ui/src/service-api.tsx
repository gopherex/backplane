import { useMemo, useState } from 'react';
import { create } from '@bufbuild/protobuf';
import { CatalogServiceClient, GetServiceRequestSchema } from '@gopherex/backplane-api';
import { RouteKind, type Manifest } from '@gopherex/backplane-api/backplanepb/v1/manifest_pb';
import { useBackplane, useClient } from '@gopherex/backplane-react';
import { Badge, Button, EmptyState, Input, NativeSelect, Panel, Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@gopherex/backplane-ui';
import { EditorActions, JSONViewer } from '@gopherex/backplane-editors';
import type { ThemeMode } from '@gopherex/backplane-theme';
import { readAPIDocument, type APIField, type APISection } from './api-document.js';
import { usePlatformText } from './locales.js';
import { QueryState, usePlatformQuery } from './runtime.js';
import { readAPIBundle, type APIFileReader } from './api-bundle.js';

export interface ServiceAPIState { route: number; operation?: string }

/** External, declared API documentation; it never invokes a service endpoint. */
export function ServiceAPI({ service, mode, state, onStateChange }: {
  service: string; mode: ThemeMode; state?: ServiceAPIState; onStateChange?: (state: ServiceAPIState) => void;
}) {
  const client = useClient(CatalogServiceClient), runtime = useBackplane(), text = usePlatformText();
  const query = usePlatformQuery(`external-api:${service}`, (signal) => client.getService(create(GetServiceRequestSchema, { name: service }), { signal }));
  return <div className="grid gap-3"><QueryState state={query} />
    <div className="flex items-center justify-between gap-3"><p className="m-0 text-sm text-muted-foreground">{text('externalAPIHelp')}</p><Button size="sm" variant="outline" onClick={query.refresh}>{text('refresh')}</Button></div>
    {query.value?.latest && <ServiceAPIDocument key={`${service}:${query.value.latest.version}`} manifest={query.value.latest} mode={mode} state={state} onStateChange={onStateChange}
      readFile={runtime.readSchemaFile ? (hash, file, signal) => runtime.readSchemaFile!(service, hash, file, signal) : undefined} />}
  </div>;
}

/** Presentational viewer for a supplied manifest, also usable by kit consumers. */
export function ServiceAPIDocument({ manifest, mode, state: controlled, onStateChange, readFile }: {
  manifest: Manifest; mode: ThemeMode; state?: ServiceAPIState; onStateChange?: (state: ServiceAPIState) => void; readFile?: APIFileReader;
}) {
  const text = usePlatformText(), [local, setLocal] = useState<ServiceAPIState>({ route: 0 });
  const state = controlled ?? local;
  const update = (next: ServiceAPIState) => { setLocal(next); onStateChange?.(next); };
  const index = Number.isSafeInteger(state.route) && manifest.routes[state.route] ? state.route : 0;
  const route = manifest.routes[index];
  const result = useMemo(() => route ? readAPIDocument(route, manifest) : undefined, [route, manifest]);
  if (!route) return <EmptyState title={text('noRoutes')} />;
  return <div className="grid min-w-0 gap-4">
    <label className="grid gap-1 text-sm">{text('externalRoute')}<NativeSelect value={index} onChange={(event) => update({ route: Number(event.target.value) })}>
      {manifest.routes.map((entry, i) => <option key={i} value={i}>{RouteKind[entry.kind]} · {entry.host}{entry.prefix}{entry.port ? ` :${entry.port}` : ''}</option>)}
    </NativeSelect></label>
    {route.schema.case === 'bundle' ? <APIBundleView key={`${route.schema.value.hash}:${route.schema.value.entry}`} bundle={route.schema.value} readFile={readFile} mode={mode} operation={state.operation} onOperation={(operation) => update({ route: index, operation })} /> : <>
      {result && 'problem' in result && <EmptyState title={text(result.problem === 'missing' ? 'apiSchemaMissing' : 'apiSchemaInvalid')} description={result.problem === 'missing' ? text('apiSchemaMissingHelp') : result.detail} />}
      {result && 'document' in result && <APIBrowser key={`${index}:${manifest.version}`} document={result.document} mode={mode} operation={state.operation} onOperation={(operation) => update({ route: index, operation })} />}
    </>}
  </div>;
}

function APIBundleView({ bundle, readFile, ...props }: { bundle: import('@gopherex/backplane-api/backplanepb/v1/manifest_pb').APISchemaBundle; readFile?: APIFileReader; mode: ThemeMode; operation?: string; onOperation: (id: string) => void }) {
  const text = usePlatformText();
  const state = usePlatformQuery(`api-files:${bundle.hash}:${bundle.format}:${bundle.entry}`, async (signal) => {
    if (!readFile) throw new Error('The host client does not provide API file delivery.');
    return readAPIBundle(bundle, readFile, signal);
  });
  return <><QueryState state={state} />{state.value && ('document' in state.value ? <APIBrowser document={state.value.document} {...props} /> : <EmptyState title={text('apiSchemaInvalid')} description={state.value.detail} />)}</>;
}

function APIBrowser({ document, mode, operation, onOperation }: {
  document: import('./api-document.js').APIDocument; mode: ThemeMode; operation?: string; onOperation: (id: string) => void;
}) {
  const text = usePlatformText(), [filter, setFilter] = useState('');
  const matching = document.operations.filter((entry) => `${entry.method} ${entry.name} ${entry.title ?? ''}`.toLowerCase().includes(filter.toLowerCase()));
  const selected = document.operations.find((entry) => entry.id === operation) ?? matching[0];
  const sectionTitle = (name: string) => name === 'parameters' ? text('apiParameters') : name === 'requestBody' ? text('apiRequest') : name === 'response' ? text('apiResponse') : name === 'security' ? text('apiSecurity') : `${text('apiResponse')} ${name}`;
  return <>
    <div className="flex flex-wrap items-center gap-2"><h2 className="m-0 text-lg font-semibold">{document.title || text('externalAPI')}</h2><Badge variant="outline">{document.format}</Badge></div>
    {document.description && <p className="m-0 whitespace-pre-wrap text-sm text-muted-foreground">{document.description}</p>}
    {document.warnings.length > 0 && <div role="status" className="rounded-md border border-border p-3 text-sm"><p>{text('apiUnresolvedRefs')}</p><ul className="list-inside list-disc">{document.warnings.map((ref) => <li className="break-all font-mono text-xs" key={ref}>{ref}</li>)}</ul></div>}
    <div className="grid min-w-0 gap-4 lg:grid-cols-[minmax(15rem,1fr)_minmax(0,3fr)]">
      <Panel title={text('apiOperations')} count={document.operations.length}>
        <Input aria-label={text('apiSearch')} placeholder={text('apiSearch')} value={filter} onChange={(event) => setFilter(event.target.value)} />
        <nav aria-label={text('apiOperations')} className="mt-2 grid max-h-[32rem] gap-1 overflow-auto">
          {matching.map((entry) => <button type="button" key={entry.id} aria-current={selected?.id === entry.id ? 'true' : undefined}
            onClick={() => onOperation(entry.id)} className={`grid gap-1 rounded-md p-2 text-left hover:bg-muted focus-visible:outline-2 focus-visible:outline-ring ${selected?.id === entry.id ? 'bg-muted' : ''}`}>
            <span className="text-xs font-semibold text-primary">{entry.method}</span><span className="break-all font-mono text-xs">{entry.name}</span>
            {entry.title && <span className="text-xs text-muted-foreground">{entry.title}</span>}
          </button>)}
          {!matching.length && <p className="text-sm text-muted-foreground">{text('apiNoOperations')}</p>}
        </nav>
      </Panel>
      {selected ? <Panel title={<span className="break-all">{selected.method} {selected.name}</span>}><div className="grid gap-4">
        {selected.title && <h3 className="m-0 text-base font-medium">{selected.title}</h3>}
        {selected.deprecated && <Badge variant="secondary">{text('apiDeprecated')}</Badge>}
        {selected.description && <p className="m-0 whitespace-pre-wrap text-sm">{selected.description}</p>}
        {selected.sections.map((section, index) => <APISectionView key={index} section={{ ...section, name: sectionTitle(section.name) }} mode={mode} />)}
      </div></Panel> : <EmptyState title={text('apiNoOperations')} />}
    </div>
    <Panel title={text('apiTypes')} count={document.schemas.length}><div className="grid gap-2">
      {document.schemas.map((schema, index) => <details className="rounded-md border border-border p-3" key={`${schema.name}:${index}`}>
        <summary className="cursor-pointer break-all font-mono text-sm">{schema.name}</summary><div className="mt-3"><APISectionView section={schema} mode={mode} /></div>
      </details>)}
    </div></Panel>
    {document.metadata && <details><summary className="cursor-pointer text-sm">{text('apiServersAuth')}</summary><JSONViewer label={text('apiServersAuth')} value={document.metadata} mode={mode} /></details>}
    <details><summary className="cursor-pointer text-sm">{text('apiDocument')}</summary><div className="mt-2 grid gap-2">
      <EditorActions value={document.source} filename="api.json" contentType="application/json" />
      <JSONViewer label={text('apiDocument')} value={document.source} mode={mode} height={400} />
    </div></details>
  </>;
}

function APISectionView({ section, mode }: { section: APISection; mode: ThemeMode }) {
  const text = usePlatformText();
  return <section className="grid min-w-0 gap-2"><h3 className="m-0 text-sm font-medium">{section.name}</h3>
    {section.description && <p className="m-0 whitespace-pre-wrap text-sm text-muted-foreground">{section.description}</p>}
    {!!section.fields?.length && <APIFields name={section.name} fields={section.fields} />}
    {section.value !== undefined && <details><summary className="cursor-pointer text-xs text-muted-foreground">{text('apiDefinition')}</summary><JSONViewer label={section.name} value={section.value} mode={mode} height={240} /></details>}
  </section>;
}

function APIFields({ fields, name }: { fields: APIField[]; name: string }) {
  const text = usePlatformText();
  return <Table aria-label={name}><TableHeader><TableRow><TableHead>{text('name')}</TableHead><TableHead>{text('apiType')}</TableHead><TableHead>{text('detail')}</TableHead></TableRow></TableHeader>
    <TableBody>{fields.map((field, index) => <TableRow key={index}><TableCell className="font-mono text-xs">{field.name}{field.required && <span className="ml-2 font-sans text-muted-foreground">{text('required')}</span>}</TableCell>
      <TableCell className="break-all font-mono text-xs">{field.type}</TableCell><TableCell className="whitespace-pre-wrap text-sm text-muted-foreground">{field.description}</TableCell></TableRow>)}</TableBody></Table>;
}
