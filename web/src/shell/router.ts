import { createRouter } from 'sv-router';
import { preloadMarkdown } from '../shared/markdown/load';
import { routeLoaded } from './firstView';
import NotFound from './NotFound.svelte';
import Unavailable from './Unavailable.svelte';

declare module 'sv-router' {
  interface RouteMeta {
    title?: string;
  }
}

const unavailable = () => ({ default: Unavailable });

const knowledge = () => preloadMarkdown(import('../features/knowledge/KnowledgePage.svelte').catch(unavailable));
const spaces = () => preloadMarkdown(import('../features/knowledge/SpacesPage.svelte').catch(unavailable));

const settingsPage = () => import('../features/settings/SettingsPage.svelte').catch(unavailable);
export const { p, navigate, isActive, route } = createRouter(
  {
    hooks: { afterLoad: routeLoaded },
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
      '/': () => import('../features/apps/AppsPage.svelte').catch(unavailable),
      '/directory': {
        '/': () => import('../features/apps/DirectoryPage.svelte').catch(unavailable),
        meta: { title: 'All apps' },
      },
      '/torrents': {
        '/': () => import('../apps/torrents/DownloadsPage.svelte').catch(unavailable),
        meta: { title: 'Downloads' },
      },
      '/media': {
        '/': () => import('../apps/library/LibraryPage.svelte').catch(unavailable),
        '/following': () => import('../apps/library/LibraryPage.svelte').catch(unavailable),
        '/intake': () => import('../apps/library/LibraryPage.svelte').catch(unavailable),
        '/item/:kind/:id': () => import('../apps/library/ItemPage.svelte').catch(unavailable),
        meta: { title: 'Library' },
      },
      '/:app': () => import('../features/apps/AppPage.svelte').catch(unavailable),
      meta: { title: 'Apps' },
    },
    '/assistant': {
      '/': () => import('../features/assistant/RoutinesPage.svelte').catch(unavailable),
      '/behavior': () => import('../features/assistant/BehaviorPage.svelte').catch(unavailable),
      '/authority': () => import('../features/assistant/AuthorityPage.svelte').catch(unavailable),
      '/spend': () => import('../features/assistant/SpendPage.svelte').catch(unavailable),
      '/routines': {
        '/': () => import('../features/assistant/RoutinesPage.svelte').catch(unavailable),
        '/:id': () => import('../features/assistant/RoutinesPage.svelte').catch(unavailable),
      },
      meta: { title: 'Assistant' },
    },
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
    '/__gallery':
      import.meta.env.MODE === 'mock'
        ? { '/': () => import('../gallery/GalleryPage.svelte').catch(unavailable), meta: { title: 'Gallery' } }
        : NotFound,
    '*': NotFound,
  },
  { base: import.meta.env.BASE_URL.replace(/\/$/, '') || undefined },
);

export function routeTitle(appName: (id: string) => string | undefined): string {
  const id = route.params.app;
  if (id) return appName(id) ?? 'Not found';
  return route.meta.title ?? 'Not found';
}
