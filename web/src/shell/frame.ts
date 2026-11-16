import { createQuery } from '@tanstack/svelte-query';
import { coreShell, ok } from '../contract';

const SHELL_TOPICS = ['review', 'attention', 'mail', 'settings', 'notify', 'automation'] as const;

export const shellQuery = {
  queryKey: ['shell'],
  queryFn: async ({ signal }: { signal: AbortSignal }) => ok(await coreShell({ signal })),
  meta: { topics: SHELL_TOPICS },
};

export function useShell(enabled: () => boolean = () => true) {
  return createQuery(() => ({ ...shellQuery, enabled: enabled() }));
}
