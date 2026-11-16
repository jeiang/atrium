import { createRouter } from 'sv-router';
import NotFound from './NotFound.svelte';
import { appById } from './nav';
import Unavailable from './Unavailable.svelte';

declare module 'sv-router' {
  interface RouteMeta {
    title?: string;
  }
}

const unavailable = () => ({ default: Unavailable });

const placeholder = () => import('./PlaceholderPage.svelte').catch(unavailable);

export const { p, navigate, isActive, route } = createRouter(
  {
    '/': { '/': () => import('../features/home/HomePage.svelte').catch(unavailable), meta: { title: 'Home' } },
    '/inbox': { '/': () => import('../features/inbox/InboxPage.svelte').catch(unavailable), meta: { title: 'Inbox' } },
    '/chat': { '/': () => import('../features/chats/ChatsPage.svelte').catch(unavailable), meta: { title: 'Chats' } },
    '/plan': { '/': placeholder, meta: { title: 'Plan' } },
    '/knowledge': { '/': placeholder, meta: { title: 'Knowledge' } },
    '/apps': {
      '/': () => import('./AppsPage.svelte').catch(unavailable),
      '/:app': () => import('./AppPage.svelte').catch(unavailable),
      meta: { title: 'Apps' },
    },
    '/assistant': { '/': placeholder, meta: { title: 'Assistant' } },
    '/settings': { '/': placeholder, meta: { title: 'Settings' } },
    '/spaces': { '/': () => import('./SpacesPage.svelte').catch(unavailable), meta: { title: 'Spaces' } },
    '/__gallery':
      import.meta.env.MODE === 'mock'
        ? { '/': () => import('../gallery/GalleryPage.svelte').catch(unavailable), meta: { title: 'Gallery' } }
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
