import { useEffect, useRef, useState } from 'react';
import { Compartment, EditorState, Transaction } from '@codemirror/state';
import { EditorView, keymap, lineNumbers, highlightActiveLine, drawSelection } from '@codemirror/view';
import { tags } from '@lezer/highlight';
import { history, historyKeymap, defaultKeymap, indentWithTab } from '@codemirror/commands';
import { syntaxHighlighting, HighlightStyle, bracketMatching } from '@codemirror/language';
import { autocompletion, completionKeymap, type CompletionContext } from '@codemirror/autocomplete';
import { linter, lintGutter, setDiagnostics, type Diagnostic } from '@codemirror/lint';
import { jsonParseLinter } from '@codemirror/lang-json';
import { tokens, typography } from '@gopherex/backplane-theme';
import { boundDiagnostics } from './model.js';
import { languageExtension, queryKeywords } from './language.js';
import { useEditorText } from './locales.js';
import type { CodeEditorProps } from './types.js';

export function editorHighlight(mode: 'dark' | 'light') {
  const c = tokens[mode];
  return syntaxHighlighting(HighlightStyle.define([
    { tag: tags.keyword, color: c.info }, { tag: [tags.string, tags.number, tags.bool, tags.null], color: c.link },
    { tag: tags.comment, color: c['muted-foreground'] }, { tag: [tags.variableName, tags.propertyName, tags.operator], color: c.foreground },
    { tag: tags.invalid, color: c.destructive },
  ]));
}
export function editorTheme(mode: 'dark' | 'light', height = 160) {
  const c = tokens[mode];
  return EditorView.theme({
    '&': { backgroundColor: c.background, color: c.foreground, height: `${height}px`, fontFamily: typography.fontFamilyMonospace },
    '.cm-scroller': { overflow: 'auto', fontFamily: 'inherit' }, '.cm-content': { padding: '8px', caretColor: c.foreground },
    '.cm-gutters': { backgroundColor: c.muted, color: c['muted-foreground'], borderColor: c.border },
    '&.cm-focused': { outline: `2px solid ${c.ring}`, outlineOffset: '2px' },
    '.cm-selectionBackground, &.cm-focused .cm-selectionBackground': { backgroundColor: c.accent },
    '.cm-activeLine': { backgroundColor: c.muted }, '.cm-cursor': { borderLeftColor: c.foreground },
    '.cm-tooltip': { backgroundColor: c.popover, color: c.foreground, border: `1px solid ${c.border}`, zIndex: '60' },
    '.cm-tooltip-autocomplete > ul > li[aria-selected]': { backgroundColor: c.accent, color: c.foreground },
  }, { dark: mode === 'dark' });
}

export default function Editor(props: CodeEditorProps) {
  const host = useRef<HTMLDivElement>(null), viewRef = useRef<EditorView | null>(null), latest = useRef(props); latest.current = props;
  const settings = useRef(new Compartment()), externalDiagnostics = useRef(new Compartment());
  const [failure, setFailure] = useState<'completionFailed' | 'validationFailed' | null>(null);
  const [messages, setMessages] = useState<readonly Diagnostic[]>([]);
  const text = useEditorText();
  useEffect(() => {
    if (!host.current) return;
    const requests = new Set<AbortController>(); let disposed = false;
    const complete = async (context: CompletionContext) => {
      const word = context.matchBefore(/[\w.$:]+/); if (!context.explicit && !word) return null;
      const controller = new AbortController(); requests.add(controller);
      context.addEventListener('abort', () => controller.abort(), { onDocChange: true });
      const language = latest.current.language ?? 'text';
      try {
        const options = latest.current.complete
          ? await latest.current.complete({ value: context.state.doc.toString(), position: context.pos, language, signal: controller.signal })
          : language === 'json' || language === 'text' ? [] : queryKeywords[language].map((label) => ({ label, type: 'keyword' }));
        if (disposed || controller.signal.aborted || context.aborted) return null;
        setFailure(null); return { from: word?.from ?? context.pos, options: [...options].slice(0, 500) };
      } catch { if (!disposed && !controller.signal.aborted) setFailure('completionFailed'); return null; }
      finally { requests.delete(controller); }
    };
    let validation: AbortController | undefined;
    const validate = async (view: EditorView): Promise<readonly Diagnostic[]> => {
      validation?.abort(); const controller = new AbortController(); validation = controller; requests.add(controller);
      const doc = view.state.doc.toString();
      try {
        const local = latest.current.language === 'json' ? jsonParseLinter()(view) : [];
        const remote = latest.current.validate ? await latest.current.validate(doc, controller.signal) : [];
        if (disposed || controller.signal.aborted || view.state.doc.toString() !== doc) return [];
        const result = [...await local, ...boundDiagnostics(remote, doc.length)];
        setMessages(result); latest.current.onDiagnostics?.(result); setFailure(null); return result;
      } catch { if (!disposed && !controller.signal.aborted) setFailure('validationFailed'); return []; }
      finally { requests.delete(controller); }
    };
    const view = new EditorView({ parent: host.current, state: EditorState.create({ doc: latest.current.value, extensions: [
      history(), drawSelection(), bracketMatching(),
      settings.current.of([]), externalDiagnostics.current.of([]),
      keymap.of([{ key: 'Mod-Enter', run: () => { if (!latest.current.disabled) latest.current.onSubmit?.(); return true; } }, ...completionKeymap, ...defaultKeymap, ...historyKeymap, indentWithTab]),
      autocompletion({ override: [complete] }), linter(validate, { delay: 300 }),
      EditorState.changeFilter.of((transaction) => !latest.current.inline || !transaction.docChanged || !transaction.newDoc.toString().includes('\n')),
      EditorView.updateListener.of((update) => { if (update.docChanged) {
        validation?.abort();
        if (!update.transactions.some((transaction) => transaction.annotation(Transaction.remote))) latest.current.onChange?.(update.state.doc.toString());
      } }),
    ] }) });
    viewRef.current = view;
    return () => { disposed = true; requests.forEach((controller) => controller.abort()); view.destroy(); viewRef.current = null; };
  }, []);
  useEffect(() => {
    const view = viewRef.current; if (!view) return;
    view.dispatch({ effects: settings.current.reconfigure([
      languageExtension(props.language ?? 'text'), editorHighlight(props.mode), editorTheme(props.mode, props.inline ? 38 : props.height),
      EditorState.readOnly.of(!!props.readOnly || !!props.disabled), EditorView.editable.of(!props.disabled),
      EditorView.contentAttributes.of({ 'aria-label': props.label, 'aria-readonly': String(!!props.readOnly), 'aria-disabled': String(!!props.disabled), tabindex: props.disabled ? '-1' : '0' }),
      props.inline ? [] : [lineNumbers(), highlightActiveLine(), lintGutter()],
    ]) });
  }, [props.language, props.mode, props.inline, props.height, props.readOnly, props.disabled, props.label]);
  useEffect(() => {
    const view = viewRef.current; if (view && view.state.doc.toString() !== props.value) view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: props.value }, annotations: Transaction.remote.of(true) });
  }, [props.value]);
  useEffect(() => { const view = viewRef.current; if (view && props.diagnostics) view.dispatch(setDiagnostics(view.state, boundDiagnostics(props.diagnostics, view.state.doc.length))); }, [props.diagnostics, props.value]);
  return <div><div ref={host} style={{ border: '1px solid var(--border)', borderRadius: 4 }} />
    <small style={{ color: 'var(--muted-foreground)' }}>{text('escapeTab')}</small>
    {failure && <p role="status">{text(failure)}</p>}
    {!!messages.length && <ul aria-label={text('diagnostics')}>{messages.map((message, index) => <li key={index}>{message.message}</li>)}</ul>}
  </div>;
}
