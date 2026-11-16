import type {
  BlockContent,
  DefinitionContent,
  ListItem,
  Nodes,
  PhrasingContent,
  RootContent,
  RowContent,
  TableRow,
} from 'mdast';
import { fromMarkdown } from 'mdast-util-from-markdown';
import { gfmAutolinkLiteralFromMarkdown } from 'mdast-util-gfm-autolink-literal';
import { gfmStrikethroughFromMarkdown } from 'mdast-util-gfm-strikethrough';
import { gfmTableFromMarkdown } from 'mdast-util-gfm-table';
import { gfmAutolinkLiteral } from 'micromark-extension-gfm-autolink-literal';
import { gfmStrikethrough } from 'micromark-extension-gfm-strikethrough';
import { gfmTable } from 'micromark-extension-gfm-table';
import { normalizeUri } from 'micromark-util-sanitize-uri';
import type { Block, Inline } from './tree';

const options = {
  extensions: [gfmTable(), gfmStrikethrough(), gfmAutolinkLiteral()],
  mdastExtensions: [gfmTableFromMarkdown(), gfmStrikethroughFromMarkdown(), gfmAutolinkLiteralFromMarkdown()],
};

interface Target {
  url: string;
  title?: string | null;
}

export function safeHref(url: string): { href: string; external: boolean } | undefined {
  const href = normalizeUri(url);
  let parsed: URL;
  try {
    parsed = new URL(href, document.baseURI);
  } catch {
    return undefined;
  }
  if (parsed.protocol === 'mailto:') return { href, external: false };
  if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') return undefined;
  return { href, external: parsed.origin !== location.origin };
}

function definitions(node: Nodes, found: Map<string, Target>): Map<string, Target> {
  if (node.type === 'definition') {
    if (!found.has(node.identifier)) found.set(node.identifier, node);
  } else if ('children' in node) {
    for (const child of node.children) definitions(child, found);
  }
  return found;
}

export function parse(source: string): Block[] {
  const root = fromMarkdown(source, options);
  const targets = definitions(root, new Map());

  const link = (target: Target | undefined, children: Inline[]): Inline[] => {
    const safe = target && safeHref(target.url);
    if (!safe) return children;
    return [{ t: 'link', href: safe.href, title: target.title ?? undefined, external: safe.external, c: children }];
  };

  const inlines = (nodes: (PhrasingContent | RowContent)[]): Inline[] =>
    nodes.flatMap((node): Inline[] => {
      switch (node.type) {
        case 'text':
        case 'html':
          return [{ t: 'text', v: node.value }];
        case 'inlineCode':
          return [{ t: 'code', v: node.value }];
        case 'break':
          return [{ t: 'br' }];
        case 'emphasis':
          return [{ t: 'em', c: inlines(node.children) }];
        case 'strong':
          return [{ t: 'strong', c: inlines(node.children) }];
        case 'delete':
          return [{ t: 'del', c: inlines(node.children) }];
        case 'link':
          return link(node, inlines(node.children));
        case 'linkReference':
          return link(targets.get(node.identifier), inlines(node.children));
        case 'image':
          return link(node, [{ t: 'text', v: node.alt || node.url }]);
        case 'imageReference': {
          const target = targets.get(node.identifier);
          return link(target, [{ t: 'text', v: node.alt || target?.url || '' }]);
        }
        default:
          return [];
      }
    });

  const row = (entry: TableRow | undefined): Inline[][] =>
    (entry?.children ?? []).map((cell) => inlines(cell.children));

  const blocks = (nodes: (RootContent | BlockContent | DefinitionContent)[], bare = false): Block[] =>
    nodes.flatMap((node): Block[] => {
      switch (node.type) {
        case 'paragraph':
          return [{ t: 'p', bare, c: inlines(node.children) }];
        case 'heading':
          return [{ t: 'h', level: node.depth, c: inlines(node.children) }];
        case 'thematicBreak':
          return [{ t: 'hr' }];
        case 'blockquote':
          return [{ t: 'quote', c: blocks(node.children) }];
        case 'code':
          return [{ t: 'code', lang: node.lang ?? '', v: node.value }];
        case 'html':
          return [{ t: 'raw', v: node.value }];
        case 'list': {
          const tight = !(
            node.spread || node.children.some((item: ListItem) => item.spread ?? item.children.length > 1)
          );
          return [
            {
              t: 'list',
              ordered: node.ordered ?? false,
              start: node.start ?? 1,
              items: node.children.map((item) => blocks(item.children, tight)),
            },
          ];
        }
        case 'table': {
          const [head, ...rows] = node.children;
          return [{ t: 'table', align: node.align ?? [], head: row(head), rows: rows.map(row) }];
        }
        default:
          return [];
      }
    });

  return blocks(root.children);
}
