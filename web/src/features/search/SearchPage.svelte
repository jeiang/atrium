<script lang="ts">
  import { createInfiniteQuery } from '@tanstack/svelte-query';
  import { API_BASE, type Hit, ok, searchAll } from '../../contract';
  import { formatDay } from '../../shared/assistant/format';
  import Badge from '../../shared/ui/Badge.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import Notice from '../../shared/ui/Notice.svelte';
  import Page from '../../shared/ui/Page.svelte';
  import Segmented from '../../shared/ui/Segmented.svelte';
  import TextField from '../../shared/ui/TextField.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';
  import { navigate, p, route } from '../../shell/router';
  import './search.css';

  const TYPES = [
    { value: '', label: 'All' },
    { value: 'thread', label: 'Chats' },
    { value: 'message', label: 'Mail' },
    { value: 'record', label: 'Knowledge' },
    { value: 'task', label: 'Tasks' },
    { value: 'file', label: 'Files' },
  ] as const;
  const GROUPS: Record<string, string> = {
    thread: 'Chats',
    message: 'Mail',
    record: 'Knowledge',
    task: 'Tasks',
    file: 'Files',
  };
  const SINGULAR: Record<string, string> = {
    thread: 'Chat',
    message: 'Email',
    record: 'Knowledge',
    task: 'Task',
    file: 'File',
  };
  const REASONS: Record<string, string> = {
    failed: 'did not answer',
    timeout: 'took too long',
    semantic_unavailable: 'only matched exact words',
  };

  const urlQuery = $derived(String(route.search.q ?? '').trim());
  const urlType = $derived(TYPES.some((item) => item.value === route.search.type) ? String(route.search.type) : '');

  let text = $state(String(route.search.q ?? ''));
  let type = $state(TYPES.some((item) => item.value === route.search.type) ? String(route.search.type) : '');
  let results = $state<HTMLElement>();

  $effect(() => {
    const q = text.trim();
    const t = type;
    if (q === urlQuery && t === urlType) return;
    const timer = setTimeout(() => {
      const search: Record<string, string> = {};
      if (q) search.q = q;
      if (t) search.type = t;
      void navigate('/search', { search, replace: true });
    }, 250);
    return () => clearTimeout(timer);
  });

  $effect(() => {
    const frame = requestAnimationFrame(() => document.querySelector<HTMLElement>('[data-search-input]')?.focus());
    return () => cancelAnimationFrame(frame);
  });

  const found = createInfiniteQuery(() => ({
    queryKey: ['search', urlQuery, urlType],
    queryFn: async ({ pageParam, signal }) =>
      ok(
        await searchAll(
          { q: urlQuery, types: urlType || undefined, limit: urlType ? 20 : 5, cursor: pageParam || undefined },
          { signal },
        ),
      ).data,
    initialPageParam: '',
    getNextPageParam: (last) => last.next_cursor ?? undefined,
    enabled: urlQuery !== '',
    meta: { topics: ['assistant', 'mail', 'knowledge', 'tasks'] },
  }));

  const first = $derived(found.data?.pages[0]);
  const partial = $derived(first?.partial ?? []);
  const top = $derived(urlType ? [] : (first?.top ?? []));
  const groups = $derived.by(() => {
    if (!found.data) return [];
    if (urlType) {
      const items = found.data.pages.flatMap((page) => page.groups.flatMap((group) => group.items));
      return [{ type: urlType, items, more: false }];
    }
    const seen = new Set(top.map((hit) => `${hit.type}:${hit.id}`));
    return (first?.groups ?? [])
      .map((group) => ({ ...group, items: group.items.filter((hit) => !seen.has(`${hit.type}:${hit.id}`)) }))
      .filter((group) => group.items.length > 0 || group.more);
  });
  const count = $derived(top.length + groups.reduce((sum, group) => sum + group.items.length, 0));
  const offline = $derived(found.fetchStatus === 'paused' && !found.data);

  function href(hit: Hit): string {
    const id = String(hit.id);
    if (hit.type === 'thread') return p('/chat/:id', { params: { id } });
    if (hit.type === 'record') return p('/knowledge/:id', { params: { id } });
    if (hit.type === 'task') return p('/tasks/:id', { params: { id } });
    if (hit.type === 'message') return `/messages/${id}`;
    return `${API_BASE}${hit.path}/content`;
  }

  function pieces(hit: Hit): { text: string; mark: boolean }[] {
    const source = hit.snippet;
    const out: { text: string; mark: boolean }[] = [];
    let at = 0;
    for (const span of [...hit.matches].sort((a, b) => a.start - b.start)) {
      const start = Math.max(at, Math.min(span.start, source.length));
      const end = Math.max(start, Math.min(span.end, source.length));
      if (start > at) out.push({ text: source.slice(at, start), mark: false });
      if (end > start) out.push({ text: source.slice(start, end), mark: true });
      at = end;
    }
    if (at < source.length) out.push({ text: source.slice(at), mark: false });
    return out;
  }

  function meta(hit: Hit): string {
    const parts = [SINGULAR[hit.type] ?? hit.type];
    if (hit.detail) parts.push(hit.detail);
    if (hit.when > 0) parts.push(formatDay(hit.when));
    if (hit.type === 'message') parts.push('opens in the current interface');
    if (hit.type === 'file') parts.push('download');
    return parts.join(' · ');
  }

  function hits(): HTMLElement[] {
    return [...(results?.querySelectorAll<HTMLElement>('[data-hit]') ?? [])];
  }

  function onInputKey(event: KeyboardEvent): void {
    if (event.key !== 'ArrowDown') return;
    const firstHit = hits()[0];
    if (!firstHit) return;
    event.preventDefault();
    firstHit.focus();
  }

  function onResultsKey(event: KeyboardEvent): void {
    const list = hits();
    const index = list.indexOf(document.activeElement as HTMLElement);
    if (index < 0) return;
    let next: HTMLElement | undefined;
    if (event.key === 'ArrowDown') next = list[index + 1];
    else if (event.key === 'ArrowUp')
      next = index === 0 ? (document.querySelector<HTMLElement>('[data-search-input]') ?? undefined) : list[index - 1];
    else if (event.key === 'Home') next = list[0];
    else if (event.key === 'End') next = list.at(-1);
    else if (event.key === 'Escape') next = document.querySelector<HTMLElement>('[data-search-input]') ?? undefined;
    else return;
    event.preventDefault();
    next?.focus();
  }

  function onShowType(value: string): void {
    type = value;
  }

  function onMore(): void {
    void found.fetchNextPage();
  }

  function onRetry(): void {
    void found.refetch();
  }
</script>

{#snippet hitRow(
    hit: Hit,
  )}
  <li>
    <a
      class="hit"
      href={href(hit)}
      onkeydown={onResultsKey}
      data-hit
      data-type={hit.type}
      download={hit.type === 'file' ? '' : undefined}
    >
      <span class="hit__title">{hit.title || hit.snippet}</span>
      {#if hit.snippet && hit.snippet !== hit.title}
        <span class="hit__snippet"
          >{#each pieces(hit) as piece, index (index)}
            {#if piece.mark}
              <mark>{piece.text}</mark>
            {:else}
              {piece.text}
            {/if}
          {/each}</span
        >
      {/if}
      <span class="hit__meta">
        {meta(hit)}
        {#if hit.private}
          <Badge>Private</Badge>
        {/if}
      </span>
    </a>
  </li>
{/snippet}

<Page title="Search">
  <div class="search">
    <search class="search__bar">
      <TextField
        data-search-input
        label="Search everything"
        type="search"
        bind:value={text}
        autocomplete="off"
        enterkeyhint="search"
        aria-keyshortcuts="Meta+K Control+K"
        onkeydown={onInputKey}
      />
      <Segmented label="Show" options={TYPES} bind:value={type} />
    </search>

    <div class="search__results" bind:this={results} aria-busy={found.isFetching || undefined}>
      {#if urlQuery === ''}
        <ViewState kind="empty" title="Search chats, mail, knowledge, tasks and files" level={2}>
          Type a few words. Press Command K or Control K from anywhere to come back here.
        </ViewState>
      {:else if offline}
        <ViewState kind="offline" title="You are offline" level={2}>
          Search works again when the connection returns.
          {#snippet action()}
            <Button onclick={onRetry}>Try again</Button>
          {/snippet}
        </ViewState>
      {:else if found.isPending}
        <ViewState kind="loading" title="Searching" />
      {:else if found.isError && !found.data}
        <ViewState kind="error" title="Search did not work" level={2}>
          {found.error.message}
          {#snippet action()}
            <Button onclick={onRetry}>Try again</Button>
          {/snippet}
        </ViewState>
      {:else}
        {#if partial.length > 0}
          <Notice tone="warn" variant="inline" title="Some results may be missing">
            {partial.map((item) => `${GROUPS[item.type] ?? item.type} ${REASONS[item.reason] ?? 'did not answer'}`).join('. ')}.
          </Notice>
        {/if}
        <p class="search__count" role="status">
          {count === 0 ? `Nothing matches “${urlQuery}”.` : `Results for “${urlQuery}”`}
        </p>
        {#if count === 0}
          <ViewState kind="empty" title="No results" level={2}>
            Try fewer or different words{urlType ? ', or search everything' : ''}.
            {#snippet action()}
              {#if urlType}
                <Button onclick={() => onShowType('')}>Search everything</Button>
              {/if}
            {/snippet}
          </ViewState>
        {/if}
        {#if top.length > 0}
          <section class="search__group" aria-labelledby="search-top">
            <h2 class="search__heading" id="search-top">Best matches</h2>
            <ul class="hits" aria-labelledby="search-top">
              {#each top as hit (`${hit.type}:${hit.id}`)}
                {@render hitRow(hit)}
              {/each}
            </ul>
          </section>
        {/if}
        {#each groups as group (group.type)}
          <section class="search__group" aria-labelledby="search-{group.type}">
            <h2 class="search__heading" id="search-{group.type}">{GROUPS[group.type] ?? group.type}</h2>
            {#if group.items.length > 0}
              <ul class="hits" aria-labelledby="search-{group.type}">
                {#each group.items as hit (`${hit.type}:${hit.id}`)}
                  {@render hitRow(hit)}
                {/each}
              </ul>
            {/if}
            {#if group.more}
              <Button size="sm" onclick={() => onShowType(group.type)}
                >More {(GROUPS[group.type] ?? group.type).toLowerCase()}</Button
              >
            {/if}
          </section>
        {/each}
        {#if urlType && found.hasNextPage}
          <Button class="search__more" pending={found.isFetchingNextPage} onclick={onMore}>Show more</Button>
        {/if}
      {/if}
    </div>
  </div>
</Page>
