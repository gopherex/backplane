export type QueryLanguage = 'cel' | 'promql' | 'metricsql' | 'logsql' | 'traceql';
export type EditorLanguage = QueryLanguage | 'json' | 'text';
export interface EditorDiagnostic { from: number; to: number; severity: 'error' | 'warning' | 'info' | 'hint'; message: string }
export interface EditorCompletion { label: string; detail?: string; type?: 'variable' | 'function' | 'keyword' | 'property'; apply?: string }
export interface CodeEditorProps {
  label: string; value: string; onChange?: (value: string) => void; language?: EditorLanguage;
  mode: 'dark' | 'light'; readOnly?: boolean; disabled?: boolean; inline?: boolean; height?: number;
  onSubmit?: () => void; diagnostics?: readonly EditorDiagnostic[];
  complete?: (request: { value: string; position: number; language: EditorLanguage; signal: AbortSignal }) => Promise<readonly EditorCompletion[]>;
  validate?: (value: string, signal: AbortSignal) => Promise<readonly EditorDiagnostic[]>;
  onDiagnostics?: (diagnostics: readonly EditorDiagnostic[]) => void;
}
export interface DiffViewerProps { label: string; before: string; after: string; mode: 'dark' | 'light'; language?: 'json' | 'text'; height?: number }
