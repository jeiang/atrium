export interface SettingsGroup {
  id: string;
  slug: string;
  title: string;
}

export type Home = 'settings' | 'assistant' | 'connections';

export const GROUPS: readonly SettingsGroup[] = [
  { id: 'settings.general', slug: 'general', title: 'General' },
  { id: 'settings.sign_in', slug: 'sign-in', title: 'Sign-in' },
  { id: 'connections', slug: 'connection-details', title: 'Connection details' },
  { id: 'apps', slug: 'apps', title: 'Apps' },
  { id: 'apps.mail', slug: 'mail', title: 'Mail' },
  { id: 'apps.calendar', slug: 'calendar', title: 'Calendar' },
  { id: 'apps.library', slug: 'library', title: 'Library' },
  { id: 'plan.options', slug: 'plan', title: 'Plan' },
  { id: 'knowledge.export', slug: 'knowledge-export', title: 'Knowledge export' },
];

export const BACKUPS: SettingsGroup = { id: 'backups', slug: 'backups', title: 'Backups' };

export const NOTIFICATIONS: SettingsGroup = { id: 'notifications', slug: 'notifications', title: 'Notifications' };

export function home(group: string): Home | undefined {
  if (GROUPS.some((item) => item.id === group)) return 'settings';
  if (group === 'assistant' || group.startsWith('assistant.')) return 'assistant';
  if (group === 'connections.weather') return 'connections';
  return undefined;
}

export interface PlacementProblem {
  key: string;
  problem: string;
}

export function placementProblems(fields: readonly { key: string; group: string }[]): PlacementProblem[] {
  const seen = new Set<string>();
  const problems: PlacementProblem[] = [];
  for (const field of fields) {
    if (seen.has(field.key)) problems.push({ key: field.key, problem: 'appears on more than one schema page' });
    seen.add(field.key);
    if (home(field.group) === undefined)
      problems.push({ key: field.key, problem: `group "${field.group}" has no page` });
  }
  return problems;
}
