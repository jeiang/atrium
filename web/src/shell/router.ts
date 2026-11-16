import { createRouter } from 'sv-router';
import NotFound from './NotFound.svelte';
import { appById } from './nav';

declare module 'sv-router' {
  interface RouteMeta {
    title?: string;
  }
}

const placeholder = () => import('./PlaceholderPage.svelte');

export const { p, navigate, isActive, route } = createRouter(
  {
    '/': { '/': () => import('../features/home/HomePage.svelte'), meta: { title: 'Home' } },
    '/inbox': {
      '/': () => import('../features/inbox/InboxPage.svelte'),
      '/:item': () => import('../features/inbox/InboxPage.svelte'),
      meta: { title: 'Inbox' },
    },
    '/proposals/:id': () => import('../features/inbox/ResolveItem.svelte'),
    '/drafts/:id': () => import('../features/inbox/ResolveItem.svelte'),
    '/chat': { '/': () => import('../features/chats/ChatsPage.svelte'), meta: { title: 'Chats' } },
    '/plan': { '/': placeholder, meta: { title: 'Plan' } },
    '/knowledge': { '/': placeholder, meta: { title: 'Knowledge' } },
    '/apps': {
      '/': () => import('./AppsPage.svelte'),
      '/:app': () => import('./AppPage.svelte'),
      meta: { title: 'Apps' },
    },
    '/assistant': { '/': placeholder, meta: { title: 'Assistant' } },
    '/settings': { '/': placeholder, meta: { title: 'Settings' } },
    '/spaces': { '/': () => import('./SpacesPage.svelte'), meta: { title: 'Spaces' } },
    '/__gallery':
      import.meta.env.MODE === 'mock'
        ? { '/': () => import('../gallery/GalleryPage.svelte'), meta: { title: 'Gallery' } }
        : NotFound,
    '*': NotFound,
  },
  { base: import.meta.env.BASE_URL.replace(/\/$/, '') || undefined },
);

export function routeTitle(): string {
  const id = route.params.app;
  if (id) return appById(id)?.title ?? 'Not found';
  return route.meta.title ?? 'Not found';
}
