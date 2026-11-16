import './tokens/gen/tokens.css';
import './tokens/fonts.css';
import './shell/shell.css';
import { mount } from 'svelte';
import { homeQuery, pendingQuery } from './features/home/queries';
import { queryClient } from './shared/query';
import { startWorker } from './shared/worker';
import App from './shell/App.svelte';
import { firstView } from './shell/firstView';
import { shellQuery } from './shell/frame';
import { loadSession } from './shell/session.svelte';

if (import.meta.env.MODE === 'mock') {
  const { startMocks } = await import('./mocks/browser');
  await startMocks();
}
void loadSession();
void queryClient.prefetchQuery(shellQuery);
if (location.pathname === import.meta.env.BASE_URL) {
  void queryClient.prefetchQuery(homeQuery);
  void queryClient.prefetchQuery(pendingQuery);
}
if (navigator.serviceWorker?.controller) void import('./tokens/faces.css');
else void firstView.then(() => import('./tokens/faces.css'));
const painted = Promise.withResolvers<void>();
requestAnimationFrame(() => setTimeout(painted.resolve));
await painted.promise;

const target = document.getElementById('app');
if (!target) throw new Error('#app is missing from index.html');
target.replaceChildren();
mount(App, { target });
if (import.meta.env.MODE !== 'mock') void startWorker();
