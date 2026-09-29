import { lazy, Suspense, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from './button.js';
import { NativeSelect } from './native-select.js';

export interface TimeRangeValue {
  /** Grafana date math (now-15m) or an ISO instant. */
  from: string; to: string; timeZone: string; weekStart?: 'monday' | 'saturday' | 'sunday';
}
export interface TimeRangeControlProps {
  value: TimeRangeValue; onChange: (value: TimeRangeValue) => void; mode: 'dark' | 'light'; label: string;
}
const Picker = lazy(() => import('./time-range.grafana.js'));
export function TimeRangeControl(props: TimeRangeControlProps) {
  const { t } = useTranslation('backplane.ui');
  return <Suspense fallback={<span role="status">{t('loading')}</span>}><Picker {...props} /></Suspense>;
}

export function RefreshControl({ interval, onIntervalChange, onRefresh, disabled }: {
  interval: number; onIntervalChange: (milliseconds: number) => void;
  onRefresh: (signal: AbortSignal) => void | Promise<void>; disabled?: boolean;
}) {
  const { t } = useTranslation('backplane.ui');
  const [pending, setPending] = useState(false), [failed, setFailed] = useState(false);
  const active = useRef<AbortController | null>(null); const refreshRef = useRef(onRefresh); refreshRef.current = onRefresh;
  const refresh = async () => {
    if (disabled || active.current) return;
    const controller = new AbortController(); active.current = controller; setPending(true); setFailed(false);
    try { await refreshRef.current(controller.signal); }
    catch { if (!controller.signal.aborted) setFailed(true); }
    finally { if (active.current === controller) { active.current = null; setPending(false); } }
  };
  const callback = useRef(refresh); callback.current = refresh;
  useEffect(() => {
    if (disabled || interval < 1000) return;
    const timer = setInterval(() => void callback.current(), interval);
    return () => clearInterval(timer);
  }, [interval, disabled]);
  useEffect(() => () => { active.current?.abort(); active.current = null; }, []);
  return <div className="flex items-center gap-2"><Button type="button" variant="outline" disabled={disabled || pending} onClick={() => void refresh()}>{t(pending ? 'loading' : 'refresh')}</Button>
    <NativeSelect aria-label={t('refreshInterval')} disabled={disabled} value={interval} onChange={(event) => onIntervalChange(Number(event.target.value))}>
      <option value={0}>{t('off')}</option>{[5000, 10000, 30000, 60000].map((value) => <option key={value} value={value}>{value / 1000} s</option>)}
    </NativeSelect>{failed && <span role="alert">{t('refreshFailed')}</span>}
  </div>;
}
