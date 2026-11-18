import { createHash } from 'node:crypto';
import { readdirSync, readFileSync } from 'node:fs';
import { join, resolve, sep } from 'node:path';
import { fileURLToPath } from 'node:url';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import { defineConfig, type Plugin, transformWithOxc } from 'vite';

const mockWorkerPath = fileURLToPath(import.meta.resolve('msw/mockServiceWorker.js'));
const lockfile = fileURLToPath(new URL('package-lock.json', import.meta.url));
const licenseFile = /^(?:licen[cs]e|copying)(?:[.-].*)?$/i;
const rule = '='.repeat(64);
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

function packageDir(root: string, id: string): string | undefined {
  const path = resolve(root, id.replace(/^\0/, '').replace(/\?.*$/, '')).split(sep).join('/');
  const at = path.lastIndexOf('/node_modules/');
  if (at < 0) return undefined;
  const [scope = '', name = ''] = path.slice(at + '/node_modules/'.length).split('/');
  return `${path.slice(0, at)}/node_modules/${scope.startsWith('@') ? `${scope}/${name}` : scope}`;
}

function thirdPartyNotices(): Plugin {
  let root = '';
  return {
    name: 'atrium-third-party-notices',
    apply: 'build',
    enforce: 'post',
    configResolved(config) {
      root = config.root;
    },
    generateBundle(_, bundle) {
      const locked = JSON.parse(readFileSync(lockfile, 'utf8')).packages as Record<string, { version?: string }>;
      const dirs = new Set<string>();
      for (const item of Object.values(bundle)) {
        for (const id of item.type === 'chunk' ? item.moduleIds : item.originalFileNames) {
          const dir = packageDir(root, id);
          if (dir) dirs.add(dir);
        }
      }
      const entries = new Map<string, string>();
      for (const dir of dirs) {
        const key = dir.slice(dir.indexOf('/node_modules/') + 1);
        const meta = JSON.parse(readFileSync(join(dir, 'package.json'), 'utf8')) as {
          name: string;
          version: string;
          license?: string;
        };
        if (locked[key]?.version !== meta.version) {
          this.error(`${meta.name} ${meta.version} is in the bundle but not at ${key} in package-lock.json`);
        }
        const file = readdirSync(dir).find((name) => licenseFile.test(name));
        if (!file) this.error(`${meta.name} ${meta.version} is in the bundle without a license file`);
        const text = readFileSync(join(dir, file), 'utf8').trim();
        entries.set(
          `${meta.name}@${meta.version}`,
          `${rule}\n${meta.name} ${meta.version} (${meta.license ?? 'see license text'})\n${rule}\n\n${text}\n`,
        );
      }
      if (entries.size === 0) this.error('no npm package found in the bundle');
      this.emitFile({
        type: 'asset',
        fileName: 'third-party-notices.txt',
        source: [...entries.keys()]
          .sort()
          .map((key) => entries.get(key))
          .join(''),
      });
    },
  };
}

export default defineConfig(({ mode }) => ({
  plugins: [
    svelte({ preprocess: { style: ({ content }) => ({ code: `${readFileSync(customMedia, 'utf8')}${content}` }) } }),
    mode === 'mock' ? undefined : thirdPartyNotices(),
    mode === 'mock' ? mockWorker() : shellWorker('/'),
  ],
  define: { __ATRIUM_CONTRACT__: JSON.stringify(contractHash) },
  css: {
    transformer: 'lightningcss',
    lightningcss: { drafts: { customMedia: true } },
  },
  resolve: {
    alias: [
      {
        find: /^decode-named-character-reference$/,
        replacement: fileURLToPath(new URL('src/shared/markdown/entities.ts', import.meta.url)),
      },
    ],
  },
  base: '/',
  server: { fs: { allow: ['.', '../docs/api/settings-schema.json'] } },
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
                  { name: 'shared', test: /[\\/]src[\\/]shared[\\/]/, minShareCount: 8 },
                ],
              },
            },
          },
    manifest: true,
  },
}));
