import type { Shell } from '../contract';
import type { IconName } from '../shared/ui/Icon.svelte';

export type DestinationPath = '/' | '/inbox' | '/plan' | '/knowledge' | '/apps' | '/assistant' | '/chat' | '/settings';

export interface Destination {
  path: DestinationPath;
  label: string;
  icon: IconName;
  module?: string;
}

export const home: Destination = { path: '/', label: 'Home', icon: 'home' };
export const inbox: Destination = { path: '/inbox', label: 'Inbox', icon: 'inbox' };
export const plan: Destination = { path: '/plan', label: 'Plan', icon: 'plan', module: 'assistant' };
export const knowledge: Destination = {
  path: '/knowledge',
  label: 'Knowledge',
  icon: 'knowledge',
  module: 'assistant',
};
export const appsDestination: Destination = { path: '/apps', label: 'Apps', icon: 'apps' };
export const assistant: Destination = {
  path: '/assistant',
  label: 'Assistant',
  icon: 'assistant',
  module: 'assistant',
};
export const chats: Destination = { path: '/chat', label: 'Chats', icon: 'chats' };
export const settings: Destination = { path: '/settings', label: 'Settings', icon: 'settings' };

export const destinations: readonly Destination[] = [home, plan, knowledge, appsDestination, assistant];

export const pages: readonly Destination[] = [
  home,
  inbox,
  plan,
  knowledge,
  appsDestination,
  assistant,
  chats,
  settings,
];

export function moduleOn(shell: Shell | undefined, module: string | undefined): boolean {
  if (!module) return true;
  if (!shell) return true;
  const state = shell.modules.find((item) => item.name === module)?.state;
  return state !== undefined && state !== 'off';
}

export function mailStatus(shell: Shell | undefined): string | undefined {
  if (!shell?.mail) return undefined;
  if (shell.mail.paused) return 'Paused';
  return shell.mail.mode === 'live' ? undefined : shell.mail.mode.charAt(0).toUpperCase() + shell.mail.mode.slice(1);
}
