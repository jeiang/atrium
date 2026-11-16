import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import { defineConfig, type Plugin } from 'vite';

const mockWorkerPath = fileURLToPath(import.meta.resolve('msw/mockServiceWorker.js'));
const fontFamilies = ['alegreya-sans', 'fira-mono'];
const customMedia = fileURLToPath(new URL('src/tokens/gen/media.css', import.meta.url));

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

function fontLicenses(): Plugin {
  return {
    name: 'atrium-font-licenses',
    generateBundle() {
      for (const family of fontFamilies) {
        const license = fileURLToPath(import.meta.resolve(`@fontsource/${family}/LICENSE`));
        this.emitFile({
          type: 'asset',
          fileName: `fonts/LICENSE-${family}.txt`,
          source: readFileSync(license, 'utf8'),
        });
      }
    },
  };
}

export default defineConfig(({ mode }) => ({
  plugins: [
    svelte({ preprocess: { style: ({ content }) => ({ code: `${readFileSync(customMedia, 'utf8')}${content}` }) } }),
    fontLicenses(),
    mode === 'mock' && mockWorker(),
  ],
  css: {
    transformer: 'lightningcss',
    lightningcss: { drafts: { customMedia: true } },
  },
  build: {
    outDir: mode === 'mock' ? 'dist-mock' : 'dist',
    target: 'baseline-widely-available',
  },
}));
