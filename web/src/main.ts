import './tokens/gen/tokens.css';
import './tokens/fonts.css';
import './shell/shell.css';
import { mount } from 'svelte';
import { startWorker } from './shared/worker';
import App from './shell/App.svelte';
import { loadSession } from './shell/session.svelte';

if (import.meta.env.MODE === 'mock') {
  const { startMocks } = await import('./mocks/browser');
  await startMocks();
}

void loadSession();
const painted = Promise.withResolvers<void>();
requestAnimationFrame(() => setTimeout(painted.resolve));
await painted.promise;

const target = document.getElementById('app');
if (!target) throw new Error('#app is missing from index.html');
target.replaceChildren();
mount(App, { target });
if (import.meta.env.MODE !== 'mock') void startWorker();
