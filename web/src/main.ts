import { mount } from 'svelte';
import App from './shell/App.svelte';
import './shell/shell.css';

if (import.meta.env.MODE === 'mock') {
  const { startMocks } = await import('./mocks/browser');
  await startMocks();
}

const target = document.getElementById('app');
if (!target) throw new Error('#app is missing from index.html');
mount(App, { target });
