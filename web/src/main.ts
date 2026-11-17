import './tokens/gen/tokens.css';
import './tokens/fonts.css';
import './shell/shell.css';
import { mount } from 'svelte';
import { homeQuery, pendingQuery } from './features/home/queries';
import { queryClient } from './shared/query';
import { startWorker, useWorker } from './shared/worker';
import App from './shell/App.svelte';
import { firstView } from './shell/firstView';
import { shellQuery } from './shell/frame';
import { loadSession, session } from './shell/session.svelte';
import faces from './tokens/faces.css?url';

if (import.meta.env.MODE === 'mock') {
  const { startMocks } = await import('./mocks/browser');
  useWorker(await startMocks());
}
const started = loadSession();
void queryClient.prefetchQuery(shellQuery);
if (location.pathname === import.meta.env.BASE_URL) {
  void queryClient.prefetchQuery(homeQuery);
  void queryClient.prefetchQuery(pendingQuery);
}
const addFaces = (): void => {
  if (session.view.status === 'redirecting') return;
  document.head.append(Object.assign(document.createElement('link'), { rel: 'stylesheet', href: faces }));
};
if (navigator.serviceWorker?.controller) addFaces();
else void Promise.all([started, firstView]).then(addFaces);
const painted = Promise.withResolvers<void>();
requestAnimationFrame(() => setTimeout(painted.resolve));
await painted.promise;

const target = document.getElementById('app');
if (!target) throw new Error('#app is missing from index.html');
target.replaceChildren();
mount(App, { target });
if (import.meta.env.MODE !== 'mock') {
  void startWorker(started.then(() => session.view.status === 'signed-in' && !session.view.cached));
}
