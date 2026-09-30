import type { EditorCompletion, EditorCompletionResult } from '@gopherex/backplane-editors';
import type * as api from '@gopherex/backplane-api';
import { scopeOf, type Scope, type WiringIndex } from './catalog.js';
import type { Definition, WiringKind } from './document.js';
import { shapeAt, shapeLabel, shapeOf, type Shape } from './shape.js';

const rootKeys: Record<WiringKind, [string, string][]> = {
  binding: [['hook', 'hook it implements'], ['description', 'text for people'], ['steps', 'calls of activities'], ['result', 'the hook output'], ['editor', 'graph layout']],
  rule: [['event', 'event it reacts to'], ['description', 'text for people'], ['when', 'CEL filter over event, meta'], ['steps', 'calls of activities'], ['editor', 'graph layout']],
};
const stepKeys: [string, string][] = [
  ['activity', '<service>.<Activity>'], ['input', 'activity input'], ['when', 'CEL bool: run only when true'], ['after', 'steps to run after'],
  ['undo', 'compensating activity'], ['undoInput', 'undo activity input'], ['retry', 'retry policy'], ['startToClose', 'attempt timeout, e.g. 30s'],
  ['heartbeat', 'heartbeat timeout'], ['description', 'text for people'],
];
const retryKeys: [string, string][] = [['attempts', 'total attempts'], ['initialInterval', 'e.g. 1s'], ['maxInterval', 'e.g. 30s'], ['backoff', 'multiplier ≥ 1']];
const functions = ['size', 'has', 'int', 'uint', 'double', 'string', 'bool', 'timestamp', 'duration', 'matches', 'startsWith', 'endsWith', 'contains', 'lowerAscii', 'upperAscii', 'trim', 'split', 'join', 'replace', 'format', 'map', 'filter', 'exists', 'all'];

/** The definition's keys along the indentation above a line: the path a key typed there belongs to. */
export function parentKeys(text: string, lineStart: number, indent: number): string[] {
  const keys: string[] = [];
  let want = indent, end = lineStart - 1;
  while (end > 0 && want > 0) {
    const start = text.lastIndexOf('\n', end - 1) + 1, line = text.slice(start, end);
    end = start - 1;
    const trimmed = line.trim();
    if (!trimmed || trimmed.startsWith('#')) continue;
    const at = line.search(/\S/);
    if (at >= want) continue;
    const key = /^\s*(?:-\s+)?([\w$-]+)\s*:/.exec(line)?.[1];
    if (key) keys.unshift(key);
    want = at;
  }
  return keys;
}

export interface CompletionContext {
  kind: WiringKind; index: WiringIndex; definition?: Definition; text: string; position: number;
}

/** Suggestions at a position of a draft's YAML. */
export function completeYAML({ kind, index, definition, text, position }: CompletionContext): EditorCompletionResult | null {
  const lineStart = text.lastIndexOf('\n', position - 1) + 1, before = text.slice(lineStart, position);
  const indent = before.search(/\S|$/);
  const listItem = /^(\s*)-\s*([\w.]*)$/.exec(before);
  const keyOnly = /^(\s*)([\w]*)$/.exec(before);
  const keyValue = /^(\s*)(?:-\s+)?([\w]+)\s*:\s*(.*)$/.exec(before);
  const steps = stepsOf(definition);
  if (listItem) {
    const path = parentKeys(text, lineStart, listItem[1]!.length);
    if (path.at(-1) === 'after') return { from: position - listItem[2]!.length, options: Object.keys(steps).map((name) => ({ label: name, type: 'variable', detail: steps[name]?.activity })) };
    return null;
  }
  if (keyOnly) {
    const path = parentKeys(text, lineStart, indent);
    const options = keysFor(kind, index, path, steps, definitionSource(kind, definition));
    return options.length ? { from: position - keyOnly[2]!.length, options } : null;
  }
  if (!keyValue) return null;
  const path = [...parentKeys(text, lineStart, keyValue[1]!.length), keyValue[2]!], rest = keyValue[3]!;
  const key = path.at(-1)!, inStep = path.length === 3 && path[0] === 'steps';
  const word = /[\w.<>-]*$/.exec(rest)![0];
  const names = (list: Map<string, { value: { description: string } }>) => [...list.entries()].map(([full, entry]) => ({ label: full, type: 'function' as const, detail: entry.value.description || undefined }));
  if (path.length === 1 && key === 'hook') return { from: position - word.length, options: names(index.hooks) };
  if (path.length === 1 && key === 'event') return { from: position - word.length, options: names(index.events) };
  if (inStep && (key === 'activity' || key === 'undo')) return { from: position - word.length, options: names(index.activities) };
  if (inStep && key === 'after') { const partial = /[\w]*$/.exec(rest)![0]; return { from: position - partial.length, options: Object.keys(steps).map((name) => ({ label: name, type: 'variable' })) }; }
  const cel = path[0] === 'result' || (path[0] === 'steps' && (path[2] === 'input' || path[2] === 'undoInput' || path[2] === 'when')) || (path.length === 1 && key === 'when');
  if (!cel) return null;
  const source = definitionSource(kind, definition);
  if (!source) return null;
  return completeCEL(rest, position, scopeOf(index, source, steps, kind === 'rule' && path.length === 1));
}

/** Keys a mapping at path may hold. */
function keysFor(kind: WiringKind, index: WiringIndex, path: readonly string[], steps: Record<string, { activity: string; undo?: string } | undefined>, source?: { hook: string } | { event: string }): EditorCompletion[] {
  const toOptions = (entries: [string, string][]) => entries.map(([label, detail]) => ({ label, detail, type: 'property' as const, apply: `${label}: ` }));
  if (!path.length) return toOptions(rootKeys[kind]);
  if (path[0] === 'steps' && path.length === 2) return toOptions(stepKeys);
  if (path[0] === 'steps' && path.length === 3 && path[2] === 'retry') return toOptions(retryKeys);
  let shape: Shape | undefined;
  if (path[0] === 'steps' && path.length >= 3 && (path[2] === 'input' || path[2] === 'undoInput')) {
    const step = steps[path[1]!], activity = path[2] === 'input' ? step?.activity : step?.undo;
    shape = shapeAt(shapeOf(activity ? index.activities.get(activity)?.value.input : undefined), path.slice(3));
  } else if (path[0] === 'result' && source && 'hook' in source) shape = shapeAt(shapeOf(index.hooks.get(source.hook)?.value.output), path.slice(1));
  return fieldOptions(shape).map((option) => ({ ...option, apply: `${option.label}: ` }));
}

function fieldOptions(shape: Shape | undefined): EditorCompletion[] {
  return (shape?.fields ?? []).map((field) => ({ label: field.name, type: 'property', detail: `${shapeLabel(field.shape)}${field.required ? ' · required' : ''}${field.description ? ` · ${field.description}` : ''}` }));
}

/** CEL suggestions at the end of `before`: variables, or the fields of the value a select chain reads. */
export function completeCEL(before: string, position: number, scope: Scope): EditorCompletionResult | null {
  const chain = /([A-Za-z_]\w*(?:\s*\.\s*[A-Za-z_]\w*)*)\s*\.\s*(\w*)$/.exec(before);
  if (chain) {
    const [root, ...fields] = chain[1]!.split('.').map((part) => part.trim());
    const variable = scope.variables.get(root!);
    if (!variable) return null;
    const shape = shapeAt(variable.shape, fields);
    return { from: position - chain[2]!.length, options: fieldOptions(shape) };
  }
  const partial = /(\w*)$/.exec(before)![1]!;
  if (/\d/.test(partial[0] ?? '')) return null;
  return { from: position - partial.length, options: [
    ...[...scope.variables.entries()].map(([name, entry]) => ({ label: name, type: 'variable' as const, detail: `${shapeLabel(entry.shape)} · ${entry.detail}` })),
    ...functions.map((name) => ({ label: name, type: 'function' as const })),
  ] };
}

export function stepsOf(definition?: Definition): Record<string, { activity: string; undo?: string } | undefined> {
  return definition?.steps ?? {};
}

export function definitionSource(kind: WiringKind, definition?: Definition): { hook: string } | { event: string } | undefined {
  if (!definition) return undefined;
  return kind === 'binding' ? { hook: (definition as api.BindingDefinition).hook } : { event: (definition as api.RuleDefinition).event };
}
