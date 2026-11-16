import { mkdirSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import StyleDictionary from 'style-dictionary';
import type { DesignToken } from 'style-dictionary/types';

interface Built {
  path: string[];
  type: string;
  value: string;
}

type Tokens = Map<string, Built>;

const root = join(import.meta.dirname, '..');
const source = join(root, '..', 'design', 'tokens');
const outDir = join(root, 'src', 'tokens', 'gen');
const cssExtension = 'org.atrium.css';
const layers = 'reset, tokens, base, shell, components, utilities';

function unitValue(value: unknown): { value: number; unit: string } | undefined {
  if (typeof value !== 'object' || value === null) return undefined;
  const { value: number, unit } = value as Record<string, unknown>;
  return typeof number === 'number' && typeof unit === 'string' ? { value: number, unit } : undefined;
}

function cssOverride(token: DesignToken): string | undefined {
  const value = (token.$extensions as Record<string, unknown> | undefined)?.[cssExtension];
  return typeof value === 'string' ? value : undefined;
}

StyleDictionary.registerTransform({
  name: 'atrium/name',
  type: 'name',
  transform: (token) => token.path.slice(1).join('-'),
});

StyleDictionary.registerTransform({
  name: 'atrium/css',
  type: 'value',
  filter: (token) => cssOverride(token) !== undefined || unitValue(token.$value) !== undefined,
  transform: (token) => {
    const override = cssOverride(token);
    if (override !== undefined) return override;
    const dimension = unitValue(token.$value);
    return dimension ? `${dimension.value}${dimension.unit}` : token.$value;
  },
});

const transforms = [
  'atrium/name',
  'atrium/css',
  'color/oklch',
  'cubicBezier/css',
  'fontFamily/css',
  'shadow/css/shorthand',
];

async function build(include: string[], files: string[]): Promise<Tokens> {
  const sd = new StyleDictionary({
    include: include.map((file) => join(source, file)),
    source: files.map((file) => join(source, file)),
    log: { warnings: 'error', verbosity: 'verbose' },
    platforms: { css: { transforms } },
  });
  const { allTokens } = await sd.getPlatformTokens('css');
  const tokens: Tokens = new Map();
  for (const token of allTokens) {
    if (tokens.has(token.name)) throw new Error(`duplicate token name ${token.name}`);
    if (!/^[a-z][a-z0-9-]*$/.test(token.name)) throw new Error(`unsafe token name ${token.name}`);
    tokens.set(token.name, { path: token.path, type: String(token.$type), value: String(token.$value) });
  }
  return tokens;
}

const colorFunction = /oklch\([^)]*\)/g;

function lightDark(name: string, light: string, dark: string): string {
  if (light === dark) return light;
  const lights = light.match(colorFunction) ?? [];
  const darks = dark.match(colorFunction) ?? [];
  if (lights.length === 0 || light.replace(colorFunction, '') !== dark.replace(colorFunction, '')) {
    throw new Error(`${name}: light and dark differ outside their colors`);
  }
  let i = 0;
  return light.replace(colorFunction, () => {
    const pair = [lights[i], darks[i]];
    i++;
    return pair[0] === pair[1] ? String(pair[0]) : `light-dark(${pair[0]}, ${pair[1]})`;
  });
}

function changed(base: Tokens, set: Tokens): [string, string][] {
  return [...set].filter(([name, token]) => base.get(name)?.value !== token.value).map(([name, t]) => [name, t.value]);
}

function oklch(text: string): [number, number, number, number] {
  const match = /^oklch\(\s*([\d.]+)(%?)\s+([\d.]+)\s+([\d.]+|none)\s*(?:\/\s*([\d.]+)(%?))?\s*\)$/.exec(text);
  if (!match) throw new Error(`cannot parse ${text}`);
  const lightness = Number(match[1]) / (match[2] ? 100 : 1);
  const hue = match[4] === 'none' ? 0 : Number(match[4]);
  const alpha = match[5] === undefined ? 1 : Number(match[5]) / (match[6] ? 100 : 1);
  return [lightness, Number(match[3]), hue, alpha];
}

function oklab([l, c, h]: number[]): [number, number, number] {
  const radians = ((h ?? 0) * Math.PI) / 180;
  return [l ?? 0, (c ?? 0) * Math.cos(radians), (c ?? 0) * Math.sin(radians)];
}

function luminance(color: string): number {
  const [l, a, b] = oklab(oklch(color));
  const lms = [
    l + 0.3963377774 * a + 0.2158037573 * b,
    l - 0.1055613458 * a - 0.0638541728 * b,
    l - 0.0894841775 * a - 1.291485548 * b,
  ].map((v) => v ** 3);
  const [x = 0, y = 0, z = 0] = lms;
  const rgb = [
    4.0767416621 * x - 3.3077115913 * y + 0.2309699292 * z,
    -1.2684380046 * x + 2.6097574011 * y - 0.3413193965 * z,
    -0.0041960863 * x - 0.7034186147 * y + 1.707614701 * z,
  ].map((v) => Math.min(1, Math.max(0, v)));
  return 0.2126 * (rgb[0] ?? 0) + 0.7152 * (rgb[1] ?? 0) + 0.0722 * (rgb[2] ?? 0);
}

function contrast(a: string, b: string): number {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return ((hi ?? 0) + 0.05) / ((lo ?? 0) + 0.05);
}

function distance(a: string, b: string): number {
  const [x, y] = [oklab(oklch(a)), oklab(oklch(b))];
  return Math.hypot(x[0] - y[0], x[1] - y[1], x[2] - y[2]);
}

function px(value: string): number {
  const first = /^clamp\(\s*([^,]+),/.exec(value)?.[1] ?? value;
  const match = /^(-?[\d.]+)(px|rem)?$/.exec(first.trim());
  if (!match) return Number.NaN;
  return Number(match[1]) * (match[2] === 'rem' ? 16 : 1);
}

const schemes = {
  light: await build([], ['base.tokens.json', 'color.light.tokens.json']),
  dark: await build([], ['base.tokens.json', 'color.dark.tokens.json']),
};
const touch = changed(
  schemes.light,
  await build(['base.tokens.json', 'color.light.tokens.json'], ['touch.tokens.json']),
);
const desktop = changed(
  schemes.light,
  await build(['base.tokens.json', 'color.light.tokens.json'], ['desktop.tokens.json']),
);

const errors: string[] = [];
const fail = (message: string) => errors.push(message);

if ([...schemes.light.keys()].join() !== [...schemes.dark.keys()].join()) fail('light and dark sets differ in tokens');

const checked: [string, string, Built | undefined][] = [
  ...[...schemes.light].map(([name, t]): [string, string, Built] => [`${name} (light)`, t.value, t]),
  ...[...schemes.dark].map(([name, t]): [string, string, Built] => [`${name} (dark)`, t.value, t]),
  ...touch.map(([name, value]): [string, string, Built | undefined] => [
    `${name} (touch)`,
    value,
    schemes.light.get(name),
  ]),
  ...desktop.map(([name, value]): [string, string, Built | undefined] => [
    `${name} (desktop)`,
    value,
    schemes.light.get(name),
  ]),
];

for (const [label, value, token] of checked) {
  const other = /#[0-9a-f]{3,8}\b|\b(?:rgba?|hsla?|hwb|lab|lch|oklab|color|color-mix)\(/i.exec(value);
  if (other)
    fail(`${label}: color "${other[0]}" is not oklch(); write colors as oklch so the palette rule can read them`);
  for (const color of value.match(colorFunction) ?? []) {
    const [, chroma, hue] = oklch(color);
    if (chroma > 0.02 && hue >= 170 && hue <= 300) {
      fail(
        `${label}: ${color} has hue ${hue} at chroma ${chroma}; no hue from 170 to 300 above chroma 0.02 (D28, D37)`,
      );
    }
  }
  if (token?.type === 'color' && !/^oklch\([^)]*\)$/.test(value))
    fail(`${label}: color token is not one oklch() value`);
  if (token?.type === 'shadow' && value.replace(colorFunction, '').replace(/-?[\d.]+(px)?|inset|,|\s/g, '') !== '') {
    fail(`${label}: shadow contains a value other than lengths and oklch() colors`);
  }
  if (token?.path[0] === 'radius' && !(px(value) > 0))
    fail(`${label}: radius ${value} must be greater than zero (D43)`);
  if (token?.path[0] === 'font' && token.type === 'dimension' && !(px(value) >= 12)) {
    fail(`${label}: font size ${value} is under the 12 px floor`);
  }
}

const pairs: [string, string, number, string?][] = [
  ['moss', 'paper', 4.5],
  ['moss', 'leaf', 4.5],
  ['moss-ink', 'moss', 4.5],
  ['moss', 'moss-soft', 4.5],
  ['ok', 'ok-soft', 4.5],
  ['ok', 'paper', 4.5],
  ['moss', 'paper-2', 3, 'focus ring'],
  ['moss', 'leaf-2', 3, 'focus ring'],
  ['line-strong', 'paper', 3, 'control boundary'],
  ['line-strong', 'leaf', 3, 'control boundary'],
  ['honey', 'honey-soft', 4.5],
  ['ink-2', 'leaf', 4.5, 'placeholder'],
  ['ink-2', 'paper', 4.5, 'placeholder'],
  ['ink-3', 'paper', 4.5],
  ['ink', 'paper', 4.5],
  ['moss-line', 'paper', 0, 'decorative only'],
];
const color = (scheme: Tokens, name: string): string => {
  const value = scheme.get(name)?.value;
  if (value === undefined) throw new Error(`missing color ${name}`);
  return value;
};

console.log('| Pair | Need | Light | Dark | Note |');
console.log('|---|---|---|---|---|');
for (const [fg, bg, need, note] of pairs) {
  const ratios = [schemes.light, schemes.dark].map((scheme) => contrast(color(scheme, fg), color(scheme, bg)));
  for (const [i, ratio] of ratios.entries()) {
    if (ratio < need) fail(`--${fg} on --${bg} (${i ? 'dark' : 'light'}) is ${ratio.toFixed(2)}:1, needs ${need}:1`);
  }
  const shown = ratios.map((ratio) => ratio.toFixed(2));
  console.log(`| --${fg} on --${bg} | ${need ? `${need}:1` : '-'} | ${shown[0]} | ${shown[1]} | ${note ?? ''} |`);
}

const apart: [string, number][] = [
  ['ok', 0.1],
  ['honey', 0.1],
  ['brick', 0.1],
  ['taupe', 0.06],
  ['d1', 0.06],
];
console.log('\n| Moss to | Need | Light | Dark |');
console.log('|---|---|---|---|');
for (const [name, need] of apart) {
  const values = [schemes.light, schemes.dark].map((scheme) => distance(color(scheme, 'moss'), color(scheme, name)));
  for (const [i, value] of values.entries()) {
    if (value < need) fail(`--moss to --${name} (${i ? 'dark' : 'light'}) is ${value.toFixed(3)} OKLab, needs ${need}`);
  }
  console.log(`| --${name} | ${need} | ${values.map((value) => value.toFixed(3)).join(' | ')} |`);
}

if (errors.length > 0) {
  console.error(`\ntokens: ${errors.length} check(s) failed`);
  for (const error of errors) console.error(`- ${error}`);
  process.exit(1);
}

const breakpoint = (name: string): string => {
  const value = schemes.light.get(name)?.value;
  if (value === undefined) throw new Error(`missing breakpoint ${name}`);
  return value;
};
const coarse = '(pointer: coarse) and (not (any-pointer: fine))';
const media = [
  `@custom-media --phone (width < ${breakpoint('desktop')});`,
  `@custom-media --desktop (width >= ${breakpoint('desktop')});`,
  `@custom-media --overlay (${breakpoint('desktop')} <= width < ${breakpoint('wide')});`,
  `@custom-media --wide (width >= ${breakpoint('wide')});`,
  `@custom-media --coarse ${coarse};`,
  `@custom-media --touch (width < ${breakpoint('desktop')}), (${coarse});`,
  '@custom-media --reduced-motion (prefers-reduced-motion: reduce);',
].join('\n');

const declarations = (entries: [string, string][], indent: string) =>
  entries.map(([name, value]) => `${indent}--${name}: ${value};`).join('\n');
const rootTokens: [string, string][] = [...schemes.light]
  .filter(([, token]) => token.path[0] !== 'breakpoint')
  .map(([name, token]) => [name, lightDark(name, token.value, color(schemes.dark, name))]);

const css = `@layer ${layers};

${media}

@layer tokens {
  :root {
    color-scheme: light dark;
${declarations(rootTokens, '    ')}
  }

  :root[data-theme="light"] {
    color-scheme: light;
  }

  :root[data-theme="dark"] {
    color-scheme: dark;
  }

  @media (--desktop) {
    :root {
${declarations(desktop, '      ')}
    }
  }

  @media (--touch) {
    :root {
${declarations(touch, '      ')}
    }
  }
}
`;

mkdirSync(outDir, { recursive: true });
writeFileSync(join(outDir, 'tokens.css'), css);
writeFileSync(join(outDir, 'media.css'), `${media}\n`);

const groups = new Map<string, number>();
for (const token of schemes.light.values()) groups.set(token.path[0] ?? '', (groups.get(token.path[0] ?? '') ?? 0) + 1);
const schemed = rootTokens.filter(([, value]) => value.includes('light-dark(')).length;
console.log(
  `\ntokens: ${schemes.light.size} tokens (${[...groups].map(([group, n]) => `${group} ${n}`).join(', ')}); ${rootTokens.length} custom properties, ${schemed} with light and dark values, ${touch.length} touch and ${desktop.length} desktop overrides`,
);
