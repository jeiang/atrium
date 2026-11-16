<script lang="ts">
  import { on } from 'svelte/events';
  import { ApiError, type KnowledgeLink, type KnowledgeMapNode } from '../../contract';
  import { announce } from '../../shared/announce.svelte';
  import { plural } from '../../shared/assistant/format';
  import Badge from '../../shared/ui/Badge.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import List from '../../shared/ui/List.svelte';
  import ListRow from '../../shared/ui/ListRow.svelte';
  import Page from '../../shared/ui/Page.svelte';
  import Segmented from '../../shared/ui/Segmented.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';
  import { navigate, p, route } from '../../shell/router';
  import {
    dialogOpen,
    KINDS,
    liveLinks,
    noteMapClosed,
    openedFromSubject,
    plainKey,
    relationLabel,
    useLinks,
    useMap,
  } from './knowledge';
  import NeighborhoodMap, { type Neighbor } from './NeighborhoodMap.svelte';
  import './knowledge.css';

  const STEP = 12;

  let phone = $state(false);
  let view = $state('map');
  let shown = $state(STEP);

  $effect(() => {
    const query = matchMedia('(width < 861px)');
    phone = query.matches;
    return on(query, 'change', () => (phone = query.matches));
  });

  const raw = $derived(String(route.search.subject ?? ''));
  const id = $derived(/^[1-9]\d*$/.test(raw) ? Number(raw) : undefined);
  const map = useMap(() => id);
  const links = useLinks(() => id);

  $effect(() => {
    void id;
    shown = STEP;
  });

  const data = $derived(map.data);
  const center = $derived(data?.nodes.find((node) => node.id === data.center));
  const neighbors = $derived(data ? arrange(data.center, data.nodes, data.edges) : []);
  const visible = $derived(neighbors.slice(0, shown));
  const groups = $derived(
    [
      { side: 'in', label: 'Links here', rows: visible.filter((item) => item.side === 'in') },
      { side: 'out', label: 'Linked from this subject', rows: visible.filter((item) => item.side === 'out') },
    ].filter((group) => group.rows.length > 0),
  );
  const elsewhere = $derived(
    [
      ...liveLinks(links.data?.outgoing).map((link) => ({ link, end: link.target, side: 'out' as const })),
      ...liveLinks(links.data?.incoming).map((link) => ({ link, end: link.source, side: 'in' as const })),
    ].filter((item) => item.end.kind !== 'record' || item.end.state !== 'live'),
  );
  const offline = $derived(map.fetchStatus === 'paused' && !data);
  const missing = $derived(map.error instanceof ApiError && map.error.status === 404);
  const showMap = $derived(!phone && view === 'map');
  const title = $derived(center ? `Map of ${center.title || 'Untitled'}` : 'Subject map');

  function edgeWords(kind: string): string {
    if (kind === 'mentions') return 'mentioned';
    if (kind === 'same_run') return 'written in the same chat';
    return relationLabel(kind).toLowerCase();
  }

  function arrange(
    centerId: number,
    nodes: readonly KnowledgeMapNode[],
    edges: readonly { from: number; to: number; kind: string }[],
  ): Neighbor[] {
    return nodes
      .filter((node) => node.id !== centerId && node.depth === 1)
      .map((node) => {
        const touching = edges.filter(
          (edge) => (edge.from === centerId && edge.to === node.id) || (edge.to === centerId && edge.from === node.id),
        );
        const inward =
          touching.length > 0 && touching.every((edge) => edge.to === centerId && edge.kind !== 'same_run');
        const kinds = [...new Set(touching.map((edge) => edge.kind))];
        const words = `${inward ? 'links here' : 'linked from this subject'}${kinds.length > 0 ? `: ${kinds.map(edgeWords).join(', ')}` : ''}`;
        return { node, side: inward ? 'in' : 'out', kinds, words };
      });
  }

  function mapHref(subject: number): string {
    return p('/knowledge/map', { search: { subject: String(subject) } });
  }

  function pageHref(subject: number): string {
    return p('/knowledge/:id', { params: { id: String(subject) } });
  }

  function endHref(end: KnowledgeLink['target']): string | undefined {
    if (end.state === 'missing') return undefined;
    if (end.kind === 'record') return p('/knowledge/:id', { params: { id: end.id } });
    if (end.kind === 'task') return p('/tasks/:id', { params: { id: end.id } });
    if (end.kind === 'space') return p('/knowledge/spaces/:space', { params: { space: end.id } });
    return undefined;
  }

  function endMeta(link: KnowledgeLink, end: KnowledgeLink['target'], side: 'in' | 'out'): string {
    const kind =
      end.kind === 'record'
        ? (KINDS[end.record_kind ?? ''] ?? 'Knowledge')
        : end.kind === 'space'
          ? 'Space'
          : end.kind === 'task'
            ? 'Task'
            : 'Contact';
    const why = end.kind === 'record' ? `${end.state}, so not on the map` : 'not a subject, so not on the map';
    return [kind, side === 'in' ? 'links here' : relationLabel(link.relation).toLowerCase(), why].join(' · ');
  }

  function close(): void {
    if (id === undefined) return;
    const back = openedFromSubject(id);
    noteMapClosed(id);
    if (back) history.back();
    else void navigate('/knowledge/:id', { params: { id: String(id) } });
  }

  function setView(next: string): void {
    view = next;
    announce(next === 'map' ? 'Showing the map' : 'Showing the list');
  }

  $effect(() =>
    on(window, 'keydown', (event) => {
      if (dialogOpen()) return;
      if (event.key === 'Escape' && !event.isComposing && id !== undefined) {
        event.preventDefault();
        close();
        return;
      }
      if (phone || !plainKey(event)) return;
      const key = event.key.toLowerCase();
      if (key !== 'm' && key !== 'l') return;
      event.preventDefault();
      setView(key === 'm' ? 'map' : 'list');
    }),
  );

  function onopen(subject: number): void {
    if (subject === data?.center) void navigate('/knowledge/:id', { params: { id: String(subject) } });
    else void navigate('/knowledge/map', { search: { subject: String(subject) } });
  }

  function onMore(): void {
    shown += STEP;
  }

  function onBack(): void {
    close();
  }

  function onRetry(): void {
    void map.refetch();
    void links.refetch();
  }

  function rowMeta(item: Neighbor): string {
    const parts = [KINDS[item.node.kind] ?? item.node.kind, item.words];
    return parts.join(' · ');
  }
</script>

{#snippet twin()}
  {#each groups as group (group.side)}
    <section class="kmap__group" aria-label={group.label}>
      <h2 class="klinks__title">{group.label} <span class="kpage__note">{group.rows.length}</span></h2>
      <List label={group.label}>
        {#each group.rows as item (item.node.id)}
          <ListRow title={item.node.title || 'Untitled'} meta={rowMeta(item)} href={pageHref(item.node.id)}>
            {#snippet end()}
              {#if !item.node.confirmed}
                <Badge tone="warn">Unconfirmed</Badge>
              {/if}
              {#if item.node.private}
                <Badge>Private</Badge>
              {/if}
              <a class="link kmap__recenter" href={mapHref(item.node.id)}
                >Map<span class="visually-hidden"> of {item.node.title || 'Untitled'}</span></a
              >
            {/snippet}
          </ListRow>
        {/each}
      </List>
    </section>
  {/each}
{/snippet}

<Page
  eyebrow="Subject map"
  {title}
  lede={center ? 'Subjects linked to this one. Escape goes back to the page.' : undefined}
>
  {#snippet actions()}
    {#if id !== undefined}
      {#if !phone && center}
        <div class="kmap__views">
          <Segmented
            label="Show the map as"
            options={[
    { value: 'map', label: 'Map (M)' },
    { value: 'list', label: 'List (L)' },
  ]}
            bind:value={() => view, setView}
          />
        </div>
      {/if}
      <Button icon="arrow" onclick={onBack} aria-keyshortcuts="Escape">Back to the page</Button>
    {/if}
  {/snippet}
  <div class="kmap">
    {#if id === undefined}
      <ViewState kind="empty" title="Pick a subject to map" level={2}>
        The map shows one subject and what links to it. Open a subject and press M.
        {#snippet action()}
          <a class="knowledge__all" href={p('/knowledge/subjects')}>All subjects</a>
        {/snippet}
      </ViewState>
    {:else if offline}
      <ViewState kind="offline" title="You are offline" level={2}>
        The map loads when the connection returns.
        {#snippet action()}
          <Button onclick={onRetry}>Try again</Button>
        {/snippet}
      </ViewState>
    {:else if map.isPending}
      <div class="kpage-loading"><ViewState kind="loading" title="Loading the map" /></div>
    {:else if missing}
      <ViewState kind="empty" title="This subject does not exist" level={2}>
        It may have been deleted. Pick another subject.
      </ViewState>
    {:else if map.isError || !data || !center}
      <ViewState kind="error" title="The map could not be loaded" level={2}>
        {map.error?.message}
        {#snippet action()}
          <Button onclick={onRetry}>Try again</Button>
        {/snippet}
      </ViewState>
    {:else}
      {#if neighbors.length === 0}
        <ViewState kind="empty" title="Nothing links to this subject yet" level={2}>
          Add a link on the page and it shows up here.
          {#snippet action()}
            <a class="knowledge__all" href={pageHref(center.id)}>Open the page</a>
          {/snippet}
        </ViewState>
      {:else}
        <p class="kpage__note" role="status">
          {neighbors.length > visible.length
    ? `Showing ${visible.length} of ${plural(neighbors.length, 'linked subject')}.`
    : plural(neighbors.length, 'linked subject')}
          {data.truncated ? ` The map stops at ${data.cap} subjects; the page lists every link.` : ''}
        </p>
        {#if showMap}
          <div class="kmap__canvas">
            <NeighborhoodMap {center} neighbors={visible} centerHref={pageHref(center.id)} href={mapHref} {onopen} />
            <p class="kmap__legend">
              <span><i class="kmap__key"></i>Linked</span>
              <span><i class="kmap__key kmap__key--mention"></i>Mentioned in the text</span>
              <span><i class="kmap__key kmap__key--run"></i>Written in the same chat</span>
              <span><i class="kmap__key kmap__key--unconfirmed"></i>Not confirmed yet</span>
            </p>
          </div>
        {:else}
          {@render twin()}
        {/if}
        {#if neighbors.length > visible.length}
          <Button class="subjects__more" onclick={onMore}>
            Show {Math.min(STEP, neighbors.length - visible.length)} more
          </Button>
        {/if}
      {/if}
      {#if elsewhere.length > 0}
        <section class="kmap__group" aria-label="Also linked">
          <h2 class="klinks__title">Also linked</h2>
          <List label="Also linked">
            {#each elsewhere as item (item.link.id)}
              <ListRow
                title={item.end.title || 'Untitled'}
                meta={endMeta(item.link, item.end, item.side)}
                href={endHref(item.end)}
              />
            {/each}
          </List>
        </section>
      {:else if links.isError}
        <p class="kpage__note">Other links could not be loaded. {links.error.message}</p>
      {/if}
    {/if}
  </div>
</Page>
