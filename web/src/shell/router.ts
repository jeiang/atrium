import { createRouter } from 'sv-router';
import NotFound from './NotFound.svelte';

export const { p, navigate, isActive, route } = createRouter({
  '/': () => import('../features/today/TodayPage.svelte'),
  '/apps': () => import('./AppsPage.svelte'),
  '*': NotFound,
});

export const pages = [
  { path: '/', title: 'Today' },
  { path: '/apps', title: 'Apps' },
] as const;
