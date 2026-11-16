import { createQuery } from '@tanstack/svelte-query';
import { coreShell, ok } from '../contract';

const SHELL_TOPICS = ['review', 'attention', 'mail', 'settings', 'notify'] as const;

export function useShell(enabled: () => boolean = () => true) {
  return createQuery(() => ({
    queryKey: ['shell'],
    queryFn: async ({ signal }) => ok(await coreShell({ signal })),
    enabled: enabled(),
    meta: { topics: SHELL_TOPICS },
  }));
}
