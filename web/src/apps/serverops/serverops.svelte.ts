import { createQuery } from '@tanstack/svelte-query';
import { ok, opsOverview, type ServerOpsHost, type ServerOpsPage } from '../../contract';
import { plural } from '../../shared/assistant/format';
import type { Tone } from '../../shared/ui/Badge.svelte';

const OPS_TOPICS = ['ops', 'knowledge', 'connections', 'settings'] as const;

export function useServerOps(enabled: () => boolean) {
  return createQuery(() => ({
    queryKey: ['server-ops'],
    queryFn: async ({ signal }: { signal: AbortSignal }) => ok(await opsOverview({ signal })).data,
    enabled: enabled(),
    meta: { topics: OPS_TOPICS },
  }));
}

export function summary(page: ServerOpsPage): string {
  if (page.hosts.length === 0) return 'No samples yet';
  const alerts = page.hosts.reduce((sum, host) => sum + host.alerts.length, 0);
  const down = page.hosts.filter((host) => host.up === false).length;
  const parts = [alerts === 0 ? 'No alerts firing' : `${plural(alerts, 'alert')} firing`];
  if (down > 0) parts.push(`${plural(down, 'host')} down`);
  return parts.join(', ');
}

export function upState(host: ServerOpsHost): { label: string; tone: Tone } {
  if (host.up === true) return { label: 'Up', tone: 'ok' };
  if (host.up === false) return { label: 'Down', tone: 'error' };
  return { label: 'No reading', tone: 'neutral' };
}

export function percent(value: number | null | undefined): string {
  return value === null || value === undefined ? 'None' : `${Math.round(value)}%`;
}

const severities: Record<string, { label: string; tone: Tone }> = {
  critical: { label: 'Critical', tone: 'error' },
  warning: { label: 'Warning', tone: 'warn' },
  info: { label: 'Info', tone: 'info' },
};

export function severityOf(severity: string): { label: string; tone: Tone } {
  return severities[severity] ?? { label: severity, tone: 'neutral' };
}
