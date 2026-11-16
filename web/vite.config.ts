import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import { defineConfig, type Plugin } from 'vite';

const mockWorkerPath = fileURLToPath(import.meta.resolve('msw/mockServiceWorker.js'));

function mockWorker(): Plugin {
  return {
    name: 'atrium-mock-worker',
    configureServer(server) {
      server.middlewares.use('/mockServiceWorker.js', (_req, res) => {
        res.setHeader('Content-Type', 'text/javascript');
        res.setHeader('Cache-Control', 'no-cache');
        res.end(readFileSync(mockWorkerPath));
      });
    },
    generateBundle() {
      this.emitFile({ type: 'asset', fileName: 'mockServiceWorker.js', source: readFileSync(mockWorkerPath, 'utf8') });
    },
  };
}

export default defineConfig(({ mode }) => ({
  plugins: [svelte(), mode === 'mock' && mockWorker()],
  build: {
    outDir: mode === 'mock' ? 'dist-mock' : 'dist',
    target: 'baseline-widely-available',
  },
}));
