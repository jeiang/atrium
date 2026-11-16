import { createRouter } from 'sv-router';
import { preloadMarkdown } from '../shared/markdown/load';
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
const knowledge = () => preloadMarkdown(import('../features/knowledge/KnowledgePage.svelte').catch(unavailable));
const spaces = () => preloadMarkdown(import('../features/knowledge/SpacesPage.svelte').catch(unavailable));

const settingsPage = () => import('../features/settings/SettingsPage.svelte').catch(unavailable);
export const { p, navigate, isActive, route } = createRouter(
  {
    '/': { '/': () => import('../features/home/HomePage.svelte').catch(unavailable), meta: { title: 'Home' } },
    '/inbox': {
      '/': () => import('../features/inbox/InboxPage.svelte').catch(unavailable),
      '/:item': () => import('../features/inbox/InboxPage.svelte').catch(unavailable),
      meta: { title: 'Inbox' },
    },
    '/proposals/:id': () => import('../features/inbox/ResolveItem.svelte').catch(unavailable),
    '/drafts/:id': () => import('../features/inbox/ResolveItem.svelte').catch(unavailable),
    '/chat': {
      '/': () => import('../features/chats/ChatsPage.svelte').catch(unavailable),
      '/new': () => preloadMarkdown(import('../features/chats/ChatPage.svelte').catch(unavailable)),
      '/:id': () => preloadMarkdown(import('../features/chats/ChatPage.svelte').catch(unavailable)),
      meta: { title: 'Chats' },
    },
    '/plan': {
      '/': () => import('../features/plan/PlanPage.svelte').catch(unavailable),
      '/waiting': () => import('../features/plan/PlanPage.svelte').catch(unavailable),
      '/item/:ref': () => import('../features/plan/PlanPage.svelte').catch(unavailable),
      '/tasks': {
        '/': () => import('../features/plan/TasksPage.svelte').catch(unavailable),
        '/:id': () => import('../features/plan/TasksPage.svelte').catch(unavailable),
        meta: { title: 'Tasks' },
      },
      meta: { title: 'Plan' },
    },
    '/knowledge': {
      '/': knowledge,
      '/subjects': knowledge,
      '/instructions': knowledge,
      '/map': () => import('../features/knowledge/MapPage.svelte').catch(unavailable),
      '/spaces': { '/': spaces, '/:space': spaces },
      '/:id': knowledge,
      meta: { title: 'Knowledge' },
    },
    '/search': {
      '/': () => import('../features/search/SearchPage.svelte').catch(unavailable),
      meta: { title: 'Search' },
    },
    '/apps': {
      '/': () => import('./AppsPage.svelte').catch(unavailable),
      '/:app': () => import('./AppPage.svelte').catch(unavailable),
      meta: { title: 'Apps' },
    },
    '/assistant': { '/': placeholder, meta: { title: 'Assistant' } },
    '/settings': {
      '/': settingsPage,
      '/:group': settingsPage,
      '/connections': {
        '/': () => import('../features/connections/ConnectionsPage.svelte').catch(unavailable),
        '/:kind/:name': () => import('../features/connections/ConnectionPage.svelte').catch(unavailable),
        meta: { title: 'Connections' },
      },
      meta: { title: 'Settings' },
    },
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
