import { memo, useCallback, useEffect, useMemo, useState, type DragEvent } from 'react';
import { applyNodeChanges, Background, BackgroundVariant, Controls, Handle, MarkerType, MiniMap, Position, ReactFlow, ReactFlowProvider, useReactFlow, type Connection, type Edge, type Node, type NodeChange, type NodeProps } from '@xyflow/react';
import { toJson } from '@bufbuild/protobuf';
import { ValueSchema } from '@bufbuild/protobuf/wkt';
import * as api from '@gopherex/backplane-api';
import { Badge, EmptyState, StatusBadge } from '@gopherex/backplane-ui';
import { CircleAlert, Clock, Filter, Flag, Play, RotateCcw, Undo2 } from 'lucide-react';
import { usePlatformText } from '../locales.js';
import { enumLabel } from '../format.js';
import { addStep, definitionSchema, editAll, removeStep, unescape, type Change, type Definition, type Draft, type DraftProblem, type WiringKind } from './document.js';
import { stepTone } from './history.js';
import { shapeLabel, shapeOf, type Shape } from './shape.js';
import type { WiringIndex } from './catalog.js';

/** Drag data of an activity dragged from the palette. */
export const activityDragType = 'application/x-backplane-activity';

const palette = ['var(--chart-1)', 'var(--chart-2)', 'var(--chart-3)', 'var(--chart-4)', 'var(--chart-5)'];
/** The color of a service, as on the system map: stable per name. */
export function serviceColor(service: string): string {
  let hash = 0;
  for (const char of service) hash = (hash * 31 + char.charCodeAt(0)) >>> 0;
  return palette[hash % palette.length]!;
}
const owner = (full: string) => full.slice(0, Math.max(0, full.indexOf('.'))) || full;

/** A port: one field of a step's input or output (or the whole value). */
interface Port { id: string; label: string; type: string; expr?: string; required?: boolean; problem?: boolean }
interface RunMark { status: api.StepRunStatus; ms?: number; attempt: number; undone: boolean }
type StepData = {
  kind: 'step'; name: string; activity: string; color: string; inputs: Port[]; outputs: Port[]; when?: string; undo?: string;
  retry?: string; problems: number; selected: boolean; run?: RunMark; level: number; conditional: boolean;
};
type EndData = { kind: 'trigger' | 'result'; title: string; subtitle: string; color: string; ports: Port[]; selected: boolean; problems: number; when?: string };
type GraphNode = Node<StepData, 'step'> | Node<EndData, 'end'>;

const TRIGGER = '$trigger', RESULT = '$result';
const WIDTH = 272, COLUMN = 340;

function ports(shape: Shape, prefix: 'in' | 'out', values?: Record<string, unknown>, problems?: (field: string) => boolean): Port[] {
  const fields = shape.kind === 'object' ? shape.fields ?? [] : [];
  const out: Port[] = fields.map((field) => ({
    id: `${prefix}:${field.name}`, label: field.name, type: shapeLabel(field.shape), required: field.required,
    expr: values ? describe(values[field.name]) : undefined, problem: problems?.(field.name),
  }));
  // Fields the value sets that the schema does not declare stay visible (the problem says why).
  if (values) for (const name of Object.keys(values)) if (!fields.some((field) => field.name === name)) out.push({ id: `${prefix}:${name}`, label: name, type: '?', expr: describe(values[name]), problem: problems?.(name) ?? true });
  return out;
}

/** A value as a port shows it: an expression as written, a literal as JSON. */
function describe(value: unknown): string | undefined {
  if (value === undefined) return undefined;
  if (typeof value === 'string') return value;
  return JSON.stringify(value);
}

function valueObject(value: unknown): Record<string, unknown> | undefined {
  return value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : undefined;
}

/** Steps without a saved position sit by level, top to bottom. */
function autoLayout(definition: Definition, levels: Map<string, number>, heights: Map<string, number>) {
  const columns = new Map<number, string[]>();
  for (const name of Object.keys(definition.steps).sort()) { const level = levels.get(name) ?? 0; columns.set(level, [...columns.get(level) ?? [], name]); }
  const at = new Map<string, { x: number; y: number }>();
  for (const [level, names] of columns) {
    let y = 0;
    for (const name of names) { at.set(name, { x: (level + 1) * COLUMN, y }); y += (heights.get(name) ?? 120) + 40; }
  }
  const maxLevel = Math.max(-1, ...columns.keys());
  return { at, resultX: (maxLevel + 2) * COLUMN };
}

export interface WiringGraphProps {
  kind: WiringKind; definition?: Definition; draft: Draft; analysis?: api.Analysis; index: WiringIndex; mode: 'dark' | 'light';
  problems: DraftProblem[]; run?: api.StepRun[]; selected?: string; onSelect: (node: string | undefined) => void;
  /** A graph edit: the draft's new text. */
  onText: (text: string) => void;
  /** Show the mini-map. */
  minimap?: boolean;
}

export function WiringGraph(props: WiringGraphProps) {
  return <ReactFlowProvider><Graph {...props} /></ReactFlowProvider>;
}

function Graph({ kind, definition, draft, analysis, index, mode, problems, run, selected, onSelect, onText, minimap }: WiringGraphProps) {
  const text = usePlatformText(), flow = useReactFlow();
  const editable = !draft.problems.some((problem) => problem.source === 'yaml' && problem.severity === 'error');
  const apply = useCallback((changes: Change[]) => { if (editable) onText(editAll(draft, changes)); }, [draft, editable, onText]);

  const graph = useMemo(() => {
    if (!definition) return { nodes: [] as GraphNode[], edges: [] as Edge[] };
    const steps = definition.steps, names = Object.keys(steps).sort();
    const levels = new Map((analysis?.steps ?? []).map((step) => [step.name, step.level]));
    const problemAt = (prefix: string) => problems.filter((problem) => problem.path === prefix || problem.path?.startsWith(`${prefix}/`)).length;
    const marks = new Map<string, RunMark>();
    for (const call of run ?? []) {
      const start = call.startedTime ? Number(call.startedTime.seconds) * 1000 + call.startedTime.nanos / 1e6 : undefined;
      const end = call.closeTime ? Number(call.closeTime.seconds) * 1000 + call.closeTime.nanos / 1e6 : undefined;
      if (call.undo) { const mark = marks.get(call.step); if (mark) mark.undone = true; continue; }
      marks.set(call.step, { status: call.status, attempt: call.attempt, ms: start !== undefined && end !== undefined ? end - start : undefined, undone: false });
    }
    const stepData = new Map<string, StepData>();
    for (const name of names) {
      const step = steps[name]!, activity = index.activities.get(step.activity)?.value;
      const input = step.input?.kind.case === 'structValue' ? valueObject(apiJson(step.input)) : undefined;
      const inputs = ports(shapeOf(activity?.input), 'in', input ?? {}, (field) => problemAt(`/steps/${name}/input/${field}`) > 0);
      if (step.input && step.input.kind.case !== 'structValue') inputs.unshift({ id: 'in:*', label: text('wholeInput'), type: activity?.input ? 'object' : 'dyn', expr: describe(apiJson(step.input)) });
      else if (!activity?.input) inputs.unshift({ id: 'in:*', label: text('wholeInput'), type: 'dyn' });
      const outputs = ports(shapeOf(activity?.output), 'out');
      outputs.unshift({ id: 'out:*', label: text('wholeOutput'), type: activity?.output ? 'object' : 'dyn' });
      const retry = step.retry?.attempts ? `×${step.retry.attempts}` : undefined;
      stepData.set(name, { kind: 'step', name, activity: step.activity, color: serviceColor(owner(step.activity)), inputs, outputs, when: step.when || undefined, undo: step.undo || undefined,
        retry, problems: problemAt(`/steps/${name}`), selected: selected === name, run: marks.get(name), level: levels.get(name) ?? -1, conditional: !!step.when });
    }
    const heights = new Map([...stepData.entries()].map(([name, data]) => [name, 58 + (data.inputs.length + data.outputs.length) * 22]));
    const layout = autoLayout(definition, levels, heights);
    const saved = definition.editor?.nodes ?? {};
    const position = (id: string, fallback: { x: number; y: number }) => saved[id] ? { x: saved[id].x, y: saved[id].y } : fallback;

    const nodes: GraphNode[] = [];
    if (kind === 'binding') {
      const hook = (definition as api.BindingDefinition).hook, contract = index.hooks.get(hook)?.value;
      const triggerPorts = ports(shapeOf(contract?.input), 'out'); triggerPorts.unshift({ id: 'out:*', label: 'req', type: contract?.input ? 'object' : 'dyn' });
      nodes.push({ id: TRIGGER, type: 'end', position: position(TRIGGER, { x: 0, y: 0 }), deletable: false, data: { kind: 'trigger', title: hook, subtitle: text('hookInput'), color: serviceColor(owner(hook)), ports: triggerPorts, selected: selected === TRIGGER, problems: problemAt('/hook') } });
      const result = valueObject(apiJson((definition as api.BindingDefinition).result));
      const resultPorts = ports(shapeOf(contract?.output), 'in', result ?? {}, (field) => problemAt(`/result/${field}`) > 0);
      if ((definition as api.BindingDefinition).result && !result) resultPorts.unshift({ id: 'in:*', label: text('wholeResult'), type: 'object', expr: describe(apiJson((definition as api.BindingDefinition).result)) });
      else if (!contract?.output) resultPorts.unshift({ id: 'in:*', label: text('wholeResult'), type: 'dyn' });
      nodes.push({ id: RESULT, type: 'end', position: position(RESULT, { x: layout.resultX, y: 0 }), deletable: false, data: { kind: 'result', title: text('resultValue'), subtitle: text('hookOutput'), color: serviceColor(owner(hook)), ports: resultPorts, selected: selected === RESULT, problems: problemAt('/result') } });
    } else {
      const event = (definition as api.RuleDefinition).event, contract = index.events.get(event)?.value;
      const triggerPorts = ports(shapeOf(contract?.schema), 'out'); triggerPorts.unshift({ id: 'out:*', label: 'event', type: contract?.schema ? 'object' : 'dyn' }); triggerPorts.push({ id: 'out:meta', label: 'meta', type: 'object' });
      nodes.push({ id: TRIGGER, type: 'end', position: position(TRIGGER, { x: 0, y: 0 }), deletable: false, data: { kind: 'trigger', title: event, subtitle: text('eventInput'), color: serviceColor(owner(event)), ports: triggerPorts, selected: selected === TRIGGER, problems: problemAt('/event') + problemAt('/when'), when: (definition as api.RuleDefinition).when || undefined } });
    }
    for (const [name, data] of stepData) nodes.push({ id: name, type: 'step', position: position(name, layout.at.get(name) ?? { x: COLUMN, y: 0 }), data });

    // Edges: every read of a variable by an expression, from its port to the port of the field it sets.
    const edges = new Map<string, Edge>();
    const has = (node: string, handle: string, side: 'in' | 'out') => {
      if (node === TRIGGER) return nodes[0]!.type === 'end' && (nodes[0]!.data as EndData).ports.some((port) => port.id === handle);
      if (node === RESULT) return nodes.some((entry) => entry.id === RESULT && (entry.data as EndData).ports.some((port) => port.id === handle));
      const data = stepData.get(node); return !!data && (side === 'in' ? data.inputs : data.outputs).some((port) => port.id === handle);
    };
    for (const reference of analysis?.references ?? []) {
      const segments = reference.path.split('/').slice(1).map(unescape);
      let target: string, targetHandle: string, control = false;
      if (segments[0] === 'steps' && segments[1] && stepData.has(segments[1])) {
        target = segments[1];
        if (segments[2] === 'input') targetHandle = segments[3] ? `in:${segments[3]}` : 'in:*';
        else if (segments[2] === 'when') { targetHandle = 'when'; control = true; }
        else continue;
      } else if (segments[0] === 'result' && kind === 'binding') { target = RESULT; targetHandle = segments[1] ? `in:${segments[1]}` : 'in:*'; }
      else continue;
      let source: string, sourceHandle: string;
      if (stepData.has(reference.variable)) { source = reference.variable; sourceHandle = reference.fields[0] ? `out:${reference.fields[0]}` : 'out:*'; }
      else if (reference.variable === 'steps' && reference.fields[0] && stepData.has(reference.fields[0])) { source = reference.fields[0]; sourceHandle = 'done'; control = true; }
      else if (reference.variable === 'req' || reference.variable === 'event') { source = TRIGGER; sourceHandle = reference.fields[0] ? `out:${reference.fields[0]}` : 'out:*'; }
      else if (reference.variable === 'meta') { source = TRIGGER; sourceHandle = 'out:meta'; }
      else continue;
      if (sourceHandle !== 'done' && !has(source, sourceHandle, 'out')) sourceHandle = 'out:*';
      if (targetHandle.startsWith('in:') && !has(target, targetHandle, 'in')) targetHandle = has(target, 'in:*', 'in') ? 'in:*' : 'head';
      const id = `${source}:${sourceHandle}->${target}:${targetHandle}`;
      if (edges.has(id)) continue;
      const color = source === TRIGGER ? (nodes[0]!.data as EndData).color : stepData.get(source)?.color ?? 'var(--subtle)';
      const mark = stepData.get(source)?.run;
      edges.set(id, { id, source, sourceHandle, target, targetHandle, type: 'smoothstep', animated: mark?.status === api.StepRunStatus.STARTED,
        style: { stroke: control ? 'var(--warning)' : color, strokeWidth: 1.5, strokeDasharray: control ? '4 4' : undefined, opacity: mark && mark.status === api.StepRunStatus.NOT_RUN ? 0.35 : 1 },
        markerEnd: { type: MarkerType.ArrowClosed, color: control ? 'var(--warning)' : color, width: 14, height: 14 },
        data: { reference: [reference.variable, ...reference.fields].join('.'), path: reference.path } });
    }
    for (const name of names) for (const after of steps[name]!.after) if (stepData.has(after)) {
      const id = `${after}:done->${name}:head`;
      edges.set(id, { id, source: after, sourceHandle: 'done', target: name, targetHandle: 'head', type: 'smoothstep', style: { stroke: 'var(--subtle)', strokeWidth: 1.25, strokeDasharray: '2 4' },
        markerEnd: { type: MarkerType.ArrowClosed, color: 'var(--subtle)', width: 12, height: 12 }, data: { after: true } });
    }
    return { nodes, edges: [...edges.values()] };
  }, [kind, definition, analysis, index, problems, run, selected, text]);

  const [nodes, setNodes] = useState<GraphNode[]>(graph.nodes);
  useEffect(() => setNodes(graph.nodes), [graph.nodes]);
  const onNodesChange = useCallback((changes: NodeChange<GraphNode>[]) => setNodes((old) => applyNodeChanges(changes, old)), []);

  const connect = useCallback((connection: Connection) => {
    if (!definition || !connection.sourceHandle || !connection.targetHandle || connection.source === connection.target) return;
    if (connection.targetHandle === 'head' && connection.sourceHandle === 'done' && connection.source !== TRIGGER) {
      const step = definition.steps[connection.target]; if (!step || step.after.includes(connection.source)) return;
      apply([{ path: ['steps', connection.target, 'after'], value: [...step.after, connection.source] }]); return;
    }
    const variable = connection.source === TRIGGER ? (connection.sourceHandle === 'out:meta' ? 'meta' : kind === 'binding' ? 'req' : 'event') : connection.source;
    const field = connection.sourceHandle.startsWith('out:') && connection.sourceHandle !== 'out:*' && connection.sourceHandle !== 'out:meta' ? connection.sourceHandle.slice(4) : undefined;
    const expr = field ? `${variable}.${field}` : variable;
    const target = connection.targetHandle.startsWith('in:') ? connection.targetHandle.slice(3) : undefined;
    if (!target) return;
    const base = connection.target === RESULT ? ['result'] : ['steps', connection.target, 'input'];
    apply([{ path: target === '*' ? base : [...base, target], value: expr }]);
  }, [definition, kind, apply]);

  const onDrop = useCallback((event: DragEvent) => {
    const activity = event.dataTransfer.getData(activityDragType); if (!activity || !editable) return;
    event.preventDefault();
    const at = flow.screenToFlowPosition({ x: event.clientX, y: event.clientY });
    const added = addStep(definition, activity, { x: at.x - WIDTH / 2, y: at.y - 20 });
    apply(added.changes); onSelect(added.name);
  }, [definition, editable, flow, apply, onSelect]);

  if (!definition) return <EmptyState className="h-full" title={text('graphNeedsYaml')} description={text('graphNeedsYamlHelp')} />;
  return <div className="relative h-full" onDragOver={(event) => { if (event.dataTransfer.types.includes(activityDragType)) { event.preventDefault(); event.dataTransfer.dropEffect = 'copy'; } }} onDrop={onDrop}>
    <ReactFlow nodes={nodes} edges={graph.edges} nodeTypes={nodeTypes} onNodesChange={onNodesChange} fitView fitViewOptions={{ padding: 0.2 }} minZoom={0.2} maxZoom={1.6}
      nodesConnectable={editable} nodesDraggable elementsSelectable deleteKeyCode={editable ? ['Delete', 'Backspace'] : null} proOptions={{ hideAttribution: true }} colorMode={mode} className="backplane-map backplane-wiring"
      onConnect={connect} onNodeClick={(_, node) => onSelect(node.id)} onPaneClick={() => onSelect(undefined)}
      onNodeDragStop={(_, node) => { if (editable) apply([{ path: ['editor', 'nodes', node.id], value: { x: Math.round(node.position.x), y: Math.round(node.position.y) } }]); }}
      onNodesDelete={(deleted) => { const changes = deleted.filter((node) => node.type === 'step').flatMap((node) => removeStep(definition, node.id)); if (changes.length) { apply(changes); onSelect(undefined); } }}
      onEdgesDelete={(deleted) => {
        const changes: Change[] = [];
        for (const edge of deleted) {
          const data = edge.data as { after?: boolean; reference?: string; path?: string } | undefined;
          if (data?.after) { const step = definition.steps[edge.target]; if (step) changes.push({ path: ['steps', edge.target, 'after'], value: step.after.filter((entry) => entry !== edge.source) }); continue; }
          // Only a port that is exactly the reference unsets; an expression that reads more stays for the author to edit.
          if (data?.path && data.reference) { const path = data.path.split('/').slice(1).map(unescape); if (valueAt(kind, definition, path) === data.reference) changes.push({ path, value: undefined }); }
        }
        if (changes.length) apply(changes);
      }}>
      <Background variant={BackgroundVariant.Dots} gap={20} size={1} color="var(--border)" />
      <Controls showInteractive={false} />
      {minimap && <MiniMap pannable zoomable className="backplane-minimap" nodeColor={(node) => (node.data as { color?: string }).color ?? 'var(--subtle)'} maskColor="color-mix(in oklab, var(--background) 70%, transparent)" style={{ width: 160, height: 100 }} />}
    </ReactFlow>
    {!editable && <div className="absolute top-2 left-1/2 -translate-x-1/2 rounded-md border border-destructive/40 bg-destructive/10 px-2 py-1 text-xs text-destructive">{text('graphReadOnly')}</div>}
  </div>;
}

/** The value at a path of the definition's JSON, as written (expressions as strings). */
function valueAt(kind: WiringKind, definition: Definition, path: readonly string[]): unknown {
  let current: unknown = toJson(definitionSchema(kind), definition);
  for (const key of path) current = current && typeof current === 'object' ? (current as Record<string, unknown>)[key] : undefined;
  return current;
}

/** A google.protobuf.Value as plain JSON. */
function apiJson(value: api.BindingDefinition['result']): unknown { return value ? toJson(ValueSchema, value) : undefined; }

const PortRow = ({ port, side }: { port: Port; side: 'in' | 'out' }) => <div className={`relative flex h-[22px] items-center gap-1.5 px-2.5 text-2xs ${side === 'out' ? 'justify-end' : ''}`}>
  {side === 'in' && <Handle id={port.id} type="target" position={Position.Left} className={`size-2! min-h-0! min-w-0! border! border-card! ${port.problem ? 'bg-destructive!' : port.expr ? 'bg-primary!' : 'bg-subtle!'}`} />}
  {side === 'in' ? <>
    <span className={`shrink-0 font-mono ${port.problem ? 'text-destructive' : 'text-foreground'}`}>{port.label}{port.required && <span className="text-destructive">*</span>}</span>
    {port.expr ? <span className="min-w-0 flex-1 truncate rounded-sm bg-raised px-1 font-mono text-link" title={port.expr}>{port.expr}</span> : <span className="min-w-0 flex-1 truncate text-muted-foreground">{port.type}</span>}
  </> : <>
    <span className="min-w-0 truncate text-muted-foreground">{port.type}</span><span className="shrink-0 font-mono text-foreground">{port.label}</span>
  </>}
  {side === 'out' && <Handle id={port.id} type="source" position={Position.Right} className="size-2! min-h-0! min-w-0! border! border-card! bg-subtle!" />}
</div>;

const StepNode = memo(function StepNode({ data }: NodeProps<Node<StepData, 'step'>>) {
  const run = data.run;
  const border = data.selected ? 'border-primary ring-1 ring-primary/40' : data.problems ? 'border-destructive/60' : 'border-border hover:border-border-strong';
  return <div className={`rounded-lg border bg-card text-card-foreground shadow-sm ${border}`} style={{ width: WIDTH }}>
    <Handle id="head" type="target" position={Position.Top} className="size-2! min-h-0! min-w-0! border-0! bg-subtle!" />
    <div className="flex items-center gap-2 rounded-t-lg border-b border-border px-2.5 py-1.5" style={{ boxShadow: `inset 3px 0 0 ${data.color}` }}>
      <span className="min-w-0 flex-1"><span className="block truncate font-mono text-xs font-medium">{data.name}</span>
        <span className="block truncate font-mono text-2xs text-muted-foreground">{data.activity || '—'}</span></span>
      {data.problems > 0 && <span className="inline-flex items-center gap-0.5 text-2xs text-destructive"><CircleAlert className="size-3" />{data.problems}</span>}
      {run && <StatusBadge tone={stepTone[run.status]} className="h-5 px-1.5 text-2xs">{run.undone ? 'undone' : enumLabel(api.StepRunStatus, run.status)}{run.ms !== undefined ? ` · ${Math.round(run.ms)}ms` : ''}</StatusBadge>}
    </div>
    {(data.when || data.undo || data.retry) && <div className="flex flex-wrap items-center gap-1 border-b border-border px-2.5 py-1">
      {data.when && <Badge variant="outline" className="max-w-full gap-1 truncate font-mono text-2xs text-warning" title={data.when}><Filter className="size-3" />{data.when}</Badge>}
      {data.undo && <Badge variant="outline" className="gap-1 font-mono text-2xs" title={data.undo}><Undo2 className="size-3" />{data.undo}</Badge>}
      {data.retry && <Badge variant="outline" className="gap-1 text-2xs"><RotateCcw className="size-3" />{data.retry}</Badge>}
    </div>}
    <Handle id="when" type="target" position={Position.Left} style={{ top: 18 }} className="size-1.5! min-h-0! min-w-0! border-0! bg-warning!" />
    <div className="py-1">{data.inputs.map((port) => <PortRow key={port.id} port={port} side="in" />)}</div>
    <div className="border-t border-border py-1">{data.outputs.map((port) => <PortRow key={port.id} port={port} side="out" />)}</div>
    <Handle id="done" type="source" position={Position.Bottom} className="size-2! min-h-0! min-w-0! border-0! bg-subtle!" />
  </div>;
});

const EndNode = memo(function EndNode({ data }: NodeProps<Node<EndData, 'end'>>) {
  const border = data.selected ? 'border-primary ring-1 ring-primary/40' : data.problems ? 'border-destructive/60' : 'border-border';
  return <div className={`rounded-lg border border-dashed bg-card text-card-foreground shadow-sm ${border}`} style={{ width: WIDTH - 32 }}>
    <div className="flex items-center gap-2 rounded-t-lg border-b border-border px-2.5 py-1.5" style={{ boxShadow: `inset 3px 0 0 ${data.color}` }}>
      <span className="grid size-5 place-items-center rounded-sm bg-raised text-muted-foreground">{data.kind === 'trigger' ? <Play className="size-3" /> : <Flag className="size-3" />}</span>
      <span className="min-w-0 flex-1"><span className="block truncate font-mono text-xs font-medium">{data.title}</span><span className="block truncate text-2xs text-muted-foreground">{data.subtitle}</span></span>
      {data.problems > 0 && <span className="inline-flex items-center gap-0.5 text-2xs text-destructive"><CircleAlert className="size-3" />{data.problems}</span>}
    </div>
    {data.when && <div className="border-b border-border px-2.5 py-1"><Badge variant="outline" className="max-w-full gap-1 truncate font-mono text-2xs text-warning" title={data.when}><Clock className="size-3" />when {data.when}</Badge></div>}
    <div className="py-1">{data.ports.map((port) => <PortRow key={port.id} port={port} side={data.kind === 'trigger' ? 'out' : 'in'} />)}</div>
  </div>;
});

const nodeTypes = { step: StepNode, end: EndNode };
