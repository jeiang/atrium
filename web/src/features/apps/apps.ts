import { ApiError, type AppEntry, type AppLink } from '../../contract';
import { refusedOffline } from '../../shared/device.svelte';

export interface Outcome {
  tone: 'warn' | 'error';
  title: string;
  text: string;
  open: AppLink[];
}

const refusalTitles: Record<string, string> = {
  core_unit: 'Always on',
  pinned: 'Set by the host',
  dependency_off: 'Needs another app',
  dependent_on: 'Another app needs it',
  needs_settings: 'Needs settings',
  unavailable: 'Unavailable',
};

export function gesture(revision: string): RequestInit {
  return { headers: { 'Idempotency-Key': crypto.randomUUID(), 'If-Match': `W/"${revision}"` } };
}

export function outcome(error: unknown, app: AppEntry): Outcome {
  if (refusedOffline(error) && error instanceof Error) {
    return { tone: 'warn', title: 'Cannot reach atrium', text: error.message, open: [] };
  }
  if (error instanceof ApiError && error.status === 412) {
    return {
      tone: 'warn',
      title: 'This changed since it loaded',
      text: 'atrium reloaded the apps. Switch again if you still want the change.',
      open: [],
    };
  }
  if (error instanceof ApiError && error.status === 409) {
    return {
      tone: 'warn',
      title: refusalTitles[error.code] ?? 'atrium refused the switch',
      text: error.message,
      open: error.code === 'dependency_off' ? app.depends.filter((item) => !item.enabled) : [],
    };
  }
  if (error instanceof ApiError) {
    return {
      tone: 'error',
      title: 'atrium refused the switch',
      text: `${error.message} Nothing was changed.`,
      open: [],
    };
  }
  return {
    tone: 'error',
    title: 'The outcome is unknown',
    text: 'The connection failed before atrium answered. atrium reloaded the apps and did not try again.',
    open: [],
  };
}

export function unmetNames(app: AppEntry): AppLink[] {
  return app.depends.filter((item) => app.unmet.includes(item.id));
}

export function names(links: readonly AppLink[]): string {
  const all = links.map((item) => item.name);
  if (all.length <= 1) return all.join('');
  return `${all.slice(0, -1).join(', ')} and ${all.at(-1)}`;
}
