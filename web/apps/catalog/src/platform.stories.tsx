import { useEffect, useMemo, useState, type ReactNode } from 'react';
import type { Meta, StoryObj } from '@storybook/react-vite';
import { BackplaneProvider } from '@gopherex/backplane-react';
import { AuditFeed, AutomationPanel, ConfigurationPanel, DeadLettersPanel, EventStreams, ExplorePanel, SchedulesPanel, ServiceCatalog, ServiceInspector, ServiceOperations, SystemMap, WiringWorkspace, WorkflowRuns, WorkflowsPanel, type WiringState } from '@gopherex/backplane-platform-ui';
import { ServiceHealth } from '@gopherex/backplane-api';
import '@xyflow/react/dist/base.css';
import { Button, Checkbox, Stack } from '@gopherex/backplane-ui';
import { PlatformFixture } from './platform-fixture';

function Fixture({ children }: { children: ReactNode }) {
  const runtime = useMemo(() => new PlatformFixture(), []), [tick, setTick] = useState(0), [fail, setFail] = useState(false);
  useEffect(() => { const timer = setInterval(() => setTick((tick) => tick + 1), 100); return () => clearInterval(timer); }, []);
  void tick;
  return <BackplaneProvider client={runtime}><Stack><h1>Platform API compositions</h1><p>Explicit local transport fixtures</p>
    <div style={{ display: 'flex', gap: 8 }}><label style={{ display: 'flex', gap: 8 }}><Checkbox checked={fail} onCheckedChange={(value) => { setFail(value === true); runtime.failWrites = value === true; }} />Simulate write failure</label>
      <Button onClick={() => runtime.reconnect()}>Reconnect fixture</Button><Button onClick={() => { runtime.gap = true; runtime.reconnect(); }}>Expire audit cursor</Button><Button onClick={() => runtime.logout()}>Replace session</Button>
    </div>{children}
    <output aria-label="Write attempts">{runtime.writes}</output><output aria-label="Sent input">{runtime.lastInput}</output><output aria-label="Watch cursors">{runtime.cursors.join(',')}</output><output aria-label="Active watches">{runtime.active}</output>
  </Stack></BackplaneProvider>;
}
function Services({ mode }: { mode: 'dark' | 'light' }) {
  const [service, setService] = useState('hello');
  return <><ServiceCatalog onSelect={(service) => setService(service.name)} /><ServiceInspector service={service} mode={mode} /></>;
}
export default { title: 'Kit/Platform', decorators: [(Story) => <Fixture><Story /></Fixture>] } satisfies Meta;
type Story = StoryObj;
const mode = (context: { globals: Record<string, unknown> }) => context.globals.theme === 'light' ? 'light' as const : 'dark' as const;
export const ServicesAndHealth: Story = { render: (_, context) => <Services mode={mode(context)} /> };
export const Configuration: Story = { render: (_, context) => <ConfigurationPanel service="hello" mode={mode(context)} /> };
export const Audit: Story = { render: (_, context) => <AuditFeed mode={mode(context)} /> };
export const Explore: Story = { render: (_, context) => <ExplorePanel mode={mode(context)} /> };
export const Operations: Story = { render: (_, context) => <ServiceOperations service="hello" mode={mode(context)} /> };
function Wiring({ mode, initial }: { mode: 'dark' | 'light'; initial: WiringState }) {
  const [state, setState] = useState<WiringState>(initial);
  return <div style={{ height: 760 }}><WiringWorkspace mode={mode} state={state} onStateChange={setState} /></div>;
}
export const WiringYaml: Story = { render: (_, context) => <Wiring mode={mode(context)} initial={{ target: { kind: 'binding', hook: 'hello.Greet' }, view: 'yaml' }} /> };
export const WiringGraph: Story = { render: (_, context) => <Wiring mode={mode(context)} initial={{ target: { kind: 'binding', hook: 'hello.Greet' }, view: 'graph' }} /> };
export const WiringRule: Story = { render: (_, context) => <Wiring mode={mode(context)} initial={{ view: 'yaml' }} /> };
export const Workflows: Story = { render: (_, context) => <WorkflowRuns service="hello" mode={mode(context)} /> };
export const Schedules: Story = { render: (_, context) => <SchedulesPanel service="hello" mode={mode(context)} /> };
export const EventsAndDeadLetters: Story = { render: (_, context) => <><EventStreams service="hello" mode={mode(context)} /><DeadLettersPanel subscriber="hello" consumer="audit" mode={mode(context)} /></> };
export const Automation: Story = { render: (_, context) => <AutomationPanel service="hello" mode={mode(context)} /> };
export const WorkflowsWorkspace: Story = { render: (_, context) => <WorkflowsPanel service="hello" mode={mode(context)} /> };
const mapServices = ['hello', 'formatter'].map((name) => ({ $typeName: 'backplane.console.v1.ServiceSummary' as const, name, latestVersion: '1.0.0', versions: ['1.0.0'], instances: 1, healthy: 1, health: ServiceHealth.HEALTHY, internalApi: false, ui: name === 'hello' }));
export const SystemMapView: Story = { render: (_, context) => <SystemMap services={mapServices} mode={mode(context)} height={420} /> };
