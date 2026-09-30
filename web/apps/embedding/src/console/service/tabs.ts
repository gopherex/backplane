export const serviceTabs = ['overview', 'configuration', 'automation', 'operations', 'events', 'workflows', 'telemetry', 'audit'] as const;
export type ServiceTab = typeof serviceTabs[number];
export const isServiceTab = (value: string): value is ServiceTab => (serviceTabs as readonly string[]).includes(value);
