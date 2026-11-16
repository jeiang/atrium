import { createRouter } from 'sv-router';
import NotFound from './NotFound.svelte';

export const { p, navigate, isActive, route } = createRouter({
  '/': () => import('../features/today/TodayPage.svelte'),
  '/apps': () => import('./AppsPage.svelte'),
  '/__gallery': import.meta.env.MODE === 'mock' ? () => import('../gallery/GalleryPage.svelte') : NotFound,
  '*': NotFound,
});

export const pages = [
  { path: '/', title: 'Today' },
  { path: '/apps', title: 'Apps' },
] as const;

const titles: Record<string, string> = {
  ...Object.fromEntries(pages.map((page) => [page.path, page.title])),
  ...(import.meta.env.MODE === 'mock' ? { '/__gallery': 'Gallery' } : {}),
};

export function routeTitle(pathname: string): string {
  return titles[pathname] ?? 'Not found';
}
