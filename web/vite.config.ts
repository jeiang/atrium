import { createHash } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import { defineConfig, type Plugin, transformWithOxc } from 'vite';

const mockWorkerPath = fileURLToPath(import.meta.resolve('msw/mockServiceWorker.js'));
const fontFamilies = ['alegreya-sans', 'fira-mono'];
const customMedia = fileURLToPath(new URL('src/tokens/gen/media.css', import.meta.url));
const workerSource = fileURLToPath(new URL('src/worker/sw.ts', import.meta.url));
const contractHash = createHash('sha256')
  .update(readFileSync(fileURLToPath(new URL('../docs/api/openapi.json', import.meta.url))))
  .digest('hex')
  .slice(0, 16);

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

function shellWorker(base: string): Plugin {
  return {
    name: 'atrium-shell-worker',
    apply: 'build',
    enforce: 'post',
    async generateBundle(_, bundle) {
      const assets = Object.keys(bundle)
        .filter((name) => name.startsWith('assets/'))
        .sort();
      const precache = [base, ...assets.map((name) => `${base}${name}`)];
      const shell = bundle['index.html'];
      if (shell?.type !== 'asset') throw new Error('atrium-shell-worker: index.html is missing from the bundle');
      const version = createHash('sha256').update(precache.join('\n')).update(shell.source).digest('hex').slice(0, 16);
      const { code } = await transformWithOxc(readFileSync(workerSource, 'utf8'), workerSource, { lang: 'ts' });
      this.emitFile({
        type: 'asset',
        fileName: 'sw.js',
        source: `const PRECACHE = ${JSON.stringify(precache)};\nconst VERSION = ${JSON.stringify(version)};\n${code}`,
      });
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
    mode === 'mock' ? mockWorker() : shellWorker('/next/'),
  ],
  define: { __ATRIUM_CONTRACT__: JSON.stringify(contractHash) },
  css: {
    transformer: 'lightningcss',
    lightningcss: { drafts: { customMedia: true } },
  },
  base: mode === 'mock' ? '/' : '/next/',
  build: {
    outDir: mode === 'mock' ? 'dist-mock' : 'dist',
    target: 'baseline-widely-available',
    rolldownOptions:
      mode === 'mock'
        ? {}
        : {
            output: {
              codeSplitting: {
                groups: [
                  { name: 'boot', tags: ['$initial'], test: /[\\/](node_modules|src[\\/](?!main\.ts))/ },
                  { name: 'shared', test: /[\\/]src[\\/]shared[\\/]/, minShareCount: 2 },
                ],
              },
            },
          },
    manifest: true,
  },
}));
