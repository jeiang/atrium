import { readFileSync, statSync } from 'node:fs';
import { extname, join, posix } from 'node:path';
import { brotliCompressSync, constants } from 'node:zlib';

interface Chunk {
  file: string;
  src?: string;
  isEntry?: boolean;
  isDynamicEntry?: boolean;
  imports?: string[];
  css?: string[];
}

interface Row {
  name: string;
  files: string[];
  measured: number;
  budget: number;
  key: string;
}

const budgetFile = 'web/budgets.json';
const budgets: Record<string, number> = JSON.parse(
  readFileSync(join(import.meta.dirname, '..', 'budgets.json'), 'utf8'),
).size;
const dist = process.argv[2] ?? 'dist';
const manifest: Record<string, Chunk> = JSON.parse(readFileSync(join(dist, '.vite', 'manifest.json'), 'utf8'));

const sizes = new Map<string, number>();
function brotli(file: string): number {
  let size = sizes.get(file);
  if (size === undefined) {
    const raw = readFileSync(join(dist, file));
    const packed = brotliCompressSync(raw, {
      params: { [constants.BROTLI_PARAM_QUALITY]: 11, [constants.BROTLI_PARAM_SIZE_HINT]: raw.length },
    });
    size = Math.min(raw.length, packed.length);
    sizes.set(file, size);
  }
  return size;
}

function closure(keys: string[]): Set<string> {
  const seen = new Set<string>();
  const walk = (key: string): void => {
    if (seen.has(key)) return;
    seen.add(key);
    for (const next of manifest[key]?.imports ?? []) walk(next);
  };
  for (const key of keys) walk(key);
  return seen;
}

function files(keys: Iterable<string>, pick: (chunk: Chunk) => string[]): string[] {
  const out = new Set<string>();
  for (const key of keys) {
    const chunk = manifest[key];
    if (chunk) for (const file of pick(chunk)) out.add(file);
  }
  return [...out].sort();
}

const sum = (list: string[], size: (file: string) => number = brotli): number =>
  list.reduce((total, file) => total + size(file), 0);

const shell = readFileSync(join(dist, 'index.html'), 'utf8');
const entryFile = /<script type="module"[^>]*src="[^"]*?\/(assets\/[^"]+\.js)"/.exec(shell)?.[1];
const entries = Object.keys(manifest).filter((key) => manifest[key]?.file === entryFile);
if (entries.length !== 1) {
  throw new Error(
    `${dist}/index.html loads ${entryFile ?? 'no module script'}, expected one chunk in .vite/manifest.json`,
  );
}

const firstLoad = closure(entries);

const routerFile = 'src/shell/router.ts';
const router = readFileSync(join(import.meta.dirname, '..', routerFile), 'utf8');
const routeModules = (pattern: RegExp): Set<string> =>
  new Set([...router.matchAll(pattern)].map((match) => posix.join(posix.dirname(routerFile), match[1] ?? '')));
const pages = [...routeModules(/import\('(\.[^']+)'\)/g)].filter((key) => manifest[key]).sort();
const withMarkdown = routeModules(/preloadMarkdown\(import\('(\.[^']+)'\)/g);
const markdown = Object.keys(manifest).filter((key) => manifest[key]?.src === 'src/shared/markdown/parse.ts');
if (pages.length === 0 || withMarkdown.size === 0 || markdown.length !== 1) {
  throw new Error(
    `${routerFile}: expected lazy pages, pages that preload markdown, and src/shared/markdown/parse.ts in .vite/manifest.json`,
  );
}
for (const key of withMarkdown) {
  if (!manifest[key]) throw new Error(`${routerFile} preloads markdown for ${key}, which .vite/manifest.json lacks`);
}
const jsOf = (chunk: Chunk): string[] => [chunk.file];
const allJs = files(Object.keys(manifest), jsOf).filter((file) => extname(file) === '.js');
const allCss = files(Object.keys(manifest), (chunk) => [...(chunk.css ?? []), chunk.file]).filter(
  (file) => extname(file) === '.css',
);
const fonts = files(Object.keys(manifest), jsOf).filter((file) =>
  ['.woff2', '.woff', '.ttf', '.otf'].includes(extname(file)),
);

const row = (name: string, key: string, list: string[], measured = sum(list)): Row => ({
  name,
  key,
  files: list,
  measured,
  budget: budgets[key] ?? Number.NaN,
});

const rows: Row[] = [
  row('shell HTML', 'shellHtml', ['index.html']),
  row('first-load JS, entry', 'firstLoadJs', files(firstLoad, jsOf)),
  row(
    'first-load CSS, entry',
    'firstLoadCss',
    files(firstLoad, (chunk) => chunk.css ?? []),
  ),
  ...pages.flatMap((key) => {
    const load = new Set([...firstLoad, ...closure([key]), ...(withMarkdown.has(key) ? closure(markdown) : [])]);
    return [
      row(`first-load JS ${key}`, 'firstLoadJs', files(load, jsOf)),
      row(
        `first-load CSS ${key}`,
        'firstLoadCss',
        files(load, (chunk) => chunk.css ?? []),
      ),
    ];
  }),
  ...Object.keys(manifest)
    .filter((key) => manifest[key]?.isDynamicEntry)
    .sort()
    .map((key) => {
      const lazy = [...closure([key])].filter((dep) => !firstLoad.has(dep));
      return row(`route ${manifest[key]?.src ?? key}`, 'routeChunk', files(lazy, jsOf));
    }),
  row('all JS', 'totalJs', allJs),
  row('all CSS', 'totalCss', allCss),
  row(
    'fonts (raw)',
    'fonts',
    fonts,
    sum(fonts, (file) => statSync(join(dist, file)).size),
  ),
];

const fmt = (n: number): string => n.toLocaleString('en-US');
const width = Math.max(...rows.map((r) => r.name.length));
console.log(`web-size: ${dist}, brotli 11 bytes, budgets in ${budgetFile}`);
console.log(`${'item'.padEnd(width)}  ${'measured'.padStart(9)}  ${'budget'.padStart(9)}  headroom`);
for (const r of rows) {
  const headroom = Number.isFinite(r.budget) ? `${(((r.budget - r.measured) / r.budget) * 100).toFixed(0)} %` : '-';
  console.log(`${r.name.padEnd(width)}  ${fmt(r.measured).padStart(9)}  ${fmt(r.budget).padStart(9)}  ${headroom}`);
}

const failures = rows.filter((r) => !(r.measured <= r.budget));
for (const r of failures) {
  console.error(
    Number.isFinite(r.budget)
      ? `web-size: ${r.name} is ${fmt(r.measured)} bytes, over the budget "${r.key}" of ${fmt(r.budget)} bytes in ${budgetFile} (${r.files.join(', ')})`
      : `web-size: ${budgetFile} has no budget "size.${r.key}"`,
  );
}
process.exit(failures.length > 0 ? 1 : 0);
