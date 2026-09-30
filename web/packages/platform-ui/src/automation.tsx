import { useCallback } from 'react';
import { create } from '@bufbuild/protobuf';
import * as api from '@gopherex/backplane-api';
import { useClient, useSnapshotWatch } from '@gopherex/backplane-react';
import { Badge, Button, EmptyState, Panel, StatusBadge, Timestamp } from '@gopherex/backplane-ui';
import { ArrowUpRight, Cable, Zap } from 'lucide-react';
import { usePlatformText } from './locales.js';
import { date, enumLabel } from './format.js';
import { bindingTone, ruleTone, type WiringTarget } from './wiring/editor.js';

/**
 * A service's wiring at a glance: its hooks with their bindings and the rules
 * on its events. Editing happens in Wiring; every row opens it there.
 */
export function AutomationPanel({ service, onOpenWiring }: { service: string; mode?: 'dark' | 'light'; onOpenWiring?: (target?: WiringTarget) => void }) {
  const bindings = useClient(api.BindingServiceClient), rules = useClient(api.RuleServiceClient), text = usePlatformText();
  const hookWatch = useSnapshotWatch(useCallback((signal: AbortSignal) => bindings.watchBindings(create(api.WatchBindingsRequestSchema, { service }), { signal }), [bindings, service]));
  const ruleWatch = useSnapshotWatch(useCallback((signal: AbortSignal) => rules.watchRules(create(api.WatchRulesRequestSchema), { signal }), [rules]));
  const hooks = hookWatch.value?.bindings ?? [];
  const own = (ruleWatch.value?.rules ?? []).filter((rule) => !rule.current?.deleted && rule.current?.definition?.event.startsWith(`${service}.`));
  const row = 'flex w-full items-center gap-3 border-b border-border px-3 py-2 text-left last:border-b-0 hover:bg-raised disabled:hover:bg-transparent';
  return <div className="grid min-h-0 gap-4 xl:grid-cols-2">
    <Panel fill flush title={<><Cable className="size-4 text-muted-foreground" />{text('bindings')}</>} count={hooks.length} description={text('bindingsHelp')}
      actions={onOpenWiring && <Button size="xs" variant="ghost" onClick={() => onOpenWiring()}>{text('openWiring')}<ArrowUpRight /></Button>}>
      {!hooks.length && <EmptyState className="py-8" title={hookWatch.status === 'loading' ? text('loading') : text('noHooks')} />}
      {hooks.map((entry) => { const steps = Object.values(entry.current?.definition?.steps ?? {}); return <button key={entry.hook} type="button" className={row} disabled={!onOpenWiring} onClick={() => onOpenWiring?.({ kind: 'binding', hook: entry.hook })}>
        <span className="min-w-0 flex-1"><span className="flex items-center gap-1.5"><span className="truncate font-mono text-sm">{entry.hook}</span>{entry.required && <Badge variant="outline">{text('required')}</Badge>}</span>
          <span className="block truncate text-xs text-muted-foreground">{entry.current && !entry.current.deleted
            ? <>v{entry.current.version.toString()} · {steps.map((step) => step.activity).join(', ') || text('noSteps')} · <Timestamp value={date(entry.current.createdAt)} /></>
            : entry.description || text('noBinding')}</span></span>
        <StatusBadge tone={bindingTone[entry.state]}>{enumLabel(api.BindingState, entry.state)}</StatusBadge>
      </button>; })}
    </Panel>
    <Panel fill flush title={<><Zap className="size-4 text-muted-foreground" />{text('rules')}</>} count={own.length} description={text('rulesHelp')}>
      {!own.length && <EmptyState className="py-8" title={ruleWatch.status === 'loading' ? text('loading') : text('noRules')} />}
      {own.map((rule) => <button key={rule.id} type="button" className={row} disabled={!onOpenWiring} onClick={() => onOpenWiring?.({ kind: 'rule', id: rule.id })}>
        <span className="min-w-0 flex-1"><span className="block truncate text-sm">{rule.current?.name || rule.id}</span>
          <span className="block truncate font-mono text-xs text-muted-foreground">{rule.current?.definition?.event} → {Object.values(rule.current?.definition?.steps ?? {}).map((step) => step.activity).join(', ')}</span></span>
        <StatusBadge tone={ruleTone[rule.state]}>{enumLabel(api.RuleState, rule.state)}</StatusBadge>
      </button>)}
    </Panel>
  </div>;
}
