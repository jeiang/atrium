import { readdirSync, readFileSync } from 'node:fs';
import { join, relative } from 'node:path';
import { parse } from 'svelte/compiler';

interface Node {
  type: string;
  start: number;
  [key: string]: unknown;
}

const root = join(import.meta.dirname, '..');
const src = join(root, 'src');
const generated = join(src, 'contract', 'gen');
const contractImport = /(^|\/)contract(\/index(\.ts)?)?$/;
const handlerName = /^(on|handle)[A-Z]|^on[a-z]+$/;
const boundaries: Record<string, true> = {
  $effect: true,
  '$effect.pre': true,
  '$effect.root': true,
  $derived: true,
  '$derived.by': true,
  $inspect: true,
  onMount: true,
  setTimeout: true,
  setInterval: true,
  requestAnimationFrame: true,
  queueMicrotask: true,
};
const htmlSinkProperties: Record<string, true> = { innerHTML: true, outerHTML: true, srcdoc: true };
const htmlSinkCalls: Record<string, true> = {
  insertAdjacentHTML: true,
  createContextualFragment: true,
  setHTMLUnsafe: true,
  'document.write': true,
  'document.writeln': true,
};
const skippedKeys: Record<string, true> = { css: true, metadata: true, options: true, comments: true, loc: true };

const isNode = (value: unknown): value is Node =>
  typeof value === 'object' && value !== null && 'type' in value && typeof value.type === 'string';

function sourceFiles(dir: string, ext: RegExp): string[] {
  return readdirSync(dir, { recursive: true, encoding: 'utf8' })
    .filter((name) => ext.test(name))
    .map((name) => join(dir, name));
}

function commandNames(): Set<string> {
  const names = new Set<string>();
  for (const file of sourceFiles(join(generated, 'endpoints'), /\.ts$/)) {
    const text = readFileSync(file, 'utf8');
    for (const m of text.matchAll(/^export const (\w+) = async \([\s\S]*?method: '([A-Z]+)'/gm)) {
      if (m[1] && m[2] !== 'GET' && m[2] !== 'HEAD') names.add(m[1]);
    }
  }
  return names;
}

function calleeName(callee: unknown): string | undefined {
  if (!isNode(callee)) return undefined;
  if (callee.type === 'Identifier') return callee.name as string;
  if (callee.type === 'MemberExpression' && !callee.computed) {
    const object = calleeName(callee.object);
    const property = calleeName(callee.property);
    return object && property ? `${object}.${property}` : undefined;
  }
  return undefined;
}

function keyName(node: Node): string | undefined {
  return isNode(node.key) && node.key.type === 'Identifier' ? (node.key.name as string) : undefined;
}

function walk(node: Node, ancestors: Node[], visit: (node: Node, ancestors: Node[]) => void): void {
  visit(node, ancestors);
  ancestors.push(node);
  for (const [key, value] of Object.entries(node)) {
    if (skippedKeys[key]) continue;
    if (Array.isArray(value)) {
      for (const item of value) if (isNode(item)) walk(item, ancestors, visit);
    } else if (isNode(value)) {
      walk(value, ancestors, visit);
    }
  }
  ancestors.pop();
}

function isHandler(fn: Node, parent: Node | undefined, grandparent: Node | undefined): boolean {
  if (fn.type === 'FunctionDeclaration') return isNode(fn.id) && handlerName.test(fn.id.name as string);
  if (!parent) return false;
  if (parent.type === 'VariableDeclarator') return isNode(parent.id) && handlerName.test(parent.id.name as string);
  if (parent.type === 'Property' || parent.type === 'MethodDefinition') {
    const key = keyName(parent);
    return key !== undefined && (handlerName.test(key) || key === 'mutationFn');
  }
  if (parent.type === 'ExpressionTag' && grandparent?.type === 'Attribute') {
    return /^on[a-z]+$/.test(grandparent.name as string);
  }
  return false;
}

function commandContext(ancestors: Node[]): string | undefined {
  for (let i = ancestors.length - 1; i >= 0; i--) {
    const node = ancestors[i] as Node;
    if (node.type === 'CallExpression') {
      const name = calleeName(node.callee);
      if (name && boundaries[name]) return `inside ${name}`;
    }
    if (
      (node.type === 'FunctionDeclaration' ||
        node.type === 'FunctionExpression' ||
        node.type === 'ArrowFunctionExpression') &&
      isHandler(node, ancestors[i - 1], ancestors[i - 2])
    ) {
      return undefined;
    }
  }
  return 'outside an event handler';
}

function lintFile(file: string, commands: Set<string>): string[] {
  const text = readFileSync(file, 'utf8');
  const prefix = file.endsWith('.svelte') ? '' : '<script lang="ts">';
  const ast = parse(prefix + text + (prefix ? '</script>' : ''), { modern: true }) as unknown as Node;
  const local = new Set<string>();
  const namespaces = new Set<string>();
  const problems: string[] = [];
  const report = (node: Node, message: string) => {
    const line = text.slice(0, node.start - prefix.length).split('\n').length;
    problems.push(`${relative(root, file)}:${line}: ${message}`);
  };

  walk(ast, [], (node) => {
    if (node.type !== 'ImportDeclaration' || !isNode(node.source)) return;
    if (!contractImport.test(node.source.value as string)) return;
    for (const spec of node.specifiers as Node[]) {
      if (spec.type === 'ImportNamespaceSpecifier' && isNode(spec.local)) namespaces.add(spec.local.name as string);
      if (spec.type === 'ImportSpecifier' && isNode(spec.imported) && isNode(spec.local)) {
        if (commands.has(spec.imported.name as string)) local.add(spec.local.name as string);
      }
    }
  });

  walk(ast, [], (node, ancestors) => {
    if (node.type === 'HtmlTag') report(node, '{@html} is an HTML sink; render through a named Trusted Types policy');
    if (node.type === 'AssignmentExpression' && isNode(node.left) && node.left.type === 'MemberExpression') {
      const property = calleeName(node.left.property);
      if (property && htmlSinkProperties[property]) report(node, `${property} is an HTML sink`);
    }
    if (node.type !== 'CallExpression') return;
    const name = calleeName(node.callee);
    if (!name) return;
    const parts = name.split('.');
    const last = parts[parts.length - 1] as string;
    if (htmlSinkCalls[name] || (parts.length > 1 && htmlSinkCalls[last])) {
      report(node, `${name} is an HTML sink`);
    }
    const command =
      (parts.length === 1 && local.has(name)) ||
      (parts.length === 2 && namespaces.has(parts[0] as string) && commands.has(last)) ||
      (parts.length > 1 && (last === 'mutate' || last === 'mutateAsync'));
    if (!command) return;
    const context = commandContext(ancestors);
    if (context) report(node, `command ${name}() is called ${context}; call commands only from event handlers`);
  });
  return problems;
}

const commands = commandNames();
if (commands.size === 0) {
  console.error(`lint-commands: no command operations found under ${relative(root, generated)}; run npm run gen`);
  process.exit(1);
}
const files = sourceFiles(src, /\.(ts|svelte)$/).filter((file) => !file.startsWith(generated));
const problems = files.flatMap((file) => lintFile(file, commands));
for (const problem of problems) console.error(problem);
if (problems.length > 0) process.exit(1);
console.log(`lint-commands: ${files.length} files, ${commands.size} command operations, no violations`);
