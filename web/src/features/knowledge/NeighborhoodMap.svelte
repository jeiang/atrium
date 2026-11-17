<script lang="ts" module>
  export interface MapSubject {
    id: number;
    title: string;
    kind: string;
    confirmed: boolean;
    private: boolean;
  }

  export interface Neighbor {
    node: MapSubject;
    side: 'in' | 'out';
    kinds: string[];
    words: string;
  }
</script>

<script lang="ts">
  interface Props {
    center: MapSubject;
    neighbors: readonly Neighbor[];
    centerHref: string;
    href: (id: number) => string;
    onopen: (id: number) => void;
  }

  let { center, neighbors, centerHref, href, onopen }: Props = $props();

  const W = 960;
  const NODE_W = 280;
  const NODE_H = 48;
  const PITCH = 60;
  const CENTER_X = (W - NODE_W) / 2;
  const RIGHT_X = W - NODE_W;
  const uid = $props.id();

  const left = $derived(neighbors.filter((item) => item.side === 'in'));
  const right = $derived(neighbors.filter((item) => item.side === 'out'));
  const rows = $derived(Math.max(left.length, right.length, 1));
  const height = $derived(rows * PITCH);
  const centerY = $derived(height / 2 - NODE_H / 2);

  function short(title: string): string {
    const text = title || 'Untitled';
    return text.length > 30 ? `${text.slice(0, 29).trimEnd()}…` : text;
  }

  function top(index: number, count: number): number {
    return (height - count * PITCH) / 2 + index * PITCH + (PITCH - NODE_H) / 2;
  }

  function edge(side: 'in' | 'out', y: number): string {
    const cy = centerY + NODE_H / 2;
    const ny = y + NODE_H / 2;
    if (side === 'in') {
      const x1 = NODE_W;
      const x2 = CENTER_X;
      const mx = (x1 + x2) / 2;
      return `M${x1},${ny} C${mx},${ny} ${mx},${cy} ${x2},${cy}`;
    }
    const x1 = CENTER_X + NODE_W;
    const x2 = RIGHT_X;
    const mx = (x1 + x2) / 2;
    return `M${x1},${cy} C${mx},${cy} ${mx},${ny} ${x2},${ny}`;
  }

  function edgeClass(kinds: readonly string[]): string {
    if (kinds.some((kind) => kind !== 'mentions' && kind !== 'same_run')) return 'kmap__edge';
    if (kinds.includes('mentions')) return 'kmap__edge kmap__edge--mention';
    return 'kmap__edge kmap__edge--run';
  }

  function nodeClass(node: MapSubject, extra = ''): string {
    return [
      'kmap__node',
      extra,
      node.confirmed ? '' : 'kmap__node--unconfirmed',
      node.private ? 'kmap__node--private' : '',
    ]
      .filter(Boolean)
      .join(' ');
  }

  function describe(item: Neighbor): string {
    const parts = [item.words];
    if (!item.node.confirmed) parts.push('unconfirmed');
    parts.push('Opens its map');
    return parts.join(', ');
  }

  // sv-router reads anchor.href, which is not a string on SVG links, so plain clicks are routed here.
  function onClick(event: MouseEvent): void {
    if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    if (!(event.currentTarget instanceof SVGAElement)) return;
    event.preventDefault();
    onopen(Number(event.currentTarget.dataset.id));
  }
</script>

{#snippet node(
    item: Neighbor,
    x: number,
    y: number,
  )}
  <a class={nodeClass(item.node)} href={href(item.node.id)} data-id={item.node.id} onclick={onClick}>
    <title>{item.node.title || 'Untitled'}{item.node.private ? ', private' : ''}</title>
    <desc>{describe(item)}</desc>
    <rect {x} {y} width={NODE_W} height={NODE_H} rx="12" />
    <text x={x + 16} y={y + NODE_H / 2 + 5}>{short(item.node.title)}{item.node.private ? ' · private' : ''}</text>
  </a>
{/snippet}

<svg class="kmap__svg" viewBox="-4 -4 {W + 8} {height + 8}" aria-labelledby="{uid}-t" aria-describedby="{uid}-d">
  <title id="{uid}-t">Map of {center.title || 'this subject'}</title>
  <desc id="{uid}-d">
    {center.title || 'This subject'}
    in the middle. Subjects that link to it are on the left; subjects it links to or was written with are on the right.
    Each one opens its own map.
  </desc>
  <g aria-hidden="true">
    {#each left as item, index (item.node.id)}
      <path class={edgeClass(item.kinds)} d={edge('in', top(index, left.length))} />
    {/each}
    {#each right as item, index (item.node.id)}
      <path class={edgeClass(item.kinds)} d={edge('out', top(index, right.length))} />
    {/each}
  </g>
  {#each left as item, index (item.node.id)}
    {@render node(item, 0, top(index, left.length))}
  {/each}
  <a class={nodeClass(center, 'kmap__node--center')} href={centerHref} data-id={center.id} onclick={onClick}>
    <title>{center.title || 'Untitled'}</title>
    <desc>The subject this map is about. Opens its page</desc>
    <rect x={CENTER_X} y={centerY} width={NODE_W} height={NODE_H} rx="14" />
    <text x={CENTER_X + 16} y={centerY + NODE_H / 2 + 5}>{short(center.title)}</text>
  </a>
  {#each right as item, index (item.node.id)}
    {@render node(item, RIGHT_X, top(index, right.length))}
  {/each}
</svg>
