<script lang="ts">
  import { plural } from '../../shared/assistant/format';
  import { device, unreachable } from '../../shared/device.svelte';
  import Badge from '../../shared/ui/Badge.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import Notice from '../../shared/ui/Notice.svelte';
  import Skeleton from '../../shared/ui/Skeleton.svelte';
  import TextField from '../../shared/ui/TextField.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';
  import { p } from '../../shell/router';
  import { useCatalogue } from './library';

  let { ready }: { ready: boolean } = $props();

  let draft = $state('');
  let q = $state('');
  const catalogue = useCatalogue(
    () => q,
    () => ready,
  );
  const pages = $derived(catalogue.data?.pages ?? []);
  const items = $derived(pages.flatMap((page) => page.items));
  const status = $derived(pages[0]?.catalogue);

  function onSearch(event: SubmitEvent): void {
    event.preventDefault();
    q = draft.trim();
  }

  function onMore(): void {
    void catalogue.fetchNextPage();
  }

  function onRetry(): void {
    void catalogue.refetch();
  }
</script>

<search>
  <form class="lib-search" onsubmit={onSearch}>
    <TextField label="Search the library" bind:value={draft} type="search" />
    <Button type="submit" variant="secondary" icon="search">Search</Button>
  </form>
</search>

{#if unreachable(catalogue) || (!device.online && !catalogue.data)}
  <ViewState kind="offline" title="Cannot reach atrium">The catalogue loads when atrium answers again.</ViewState>
{:else if catalogue.isError && !catalogue.data}
  <ViewState kind="error" title="The catalogue did not load">
    {catalogue.error.message}
    {#snippet action()}
      <Button variant="secondary" onclick={onRetry}>Retry</Button>
    {/snippet}
  </ViewState>
{:else if !catalogue.data}
  <ul class="lib-grid" aria-label="Loading the catalogue">
    {#each [0, 1, 2, 3] as key (key)}
      <li class="lib-grid__skeleton"><Skeleton lines={2} label="Loading the catalogue" /></li>
    {/each}
  </ul>
{:else}
  {#if status?.stale}
    <Notice tone="warn" title="The catalogue may be out of date">
      The library server did not answer the last scan, so this is the list from
      {new Date(status.observed_at).toLocaleString()}.
    </Notice>
  {/if}
  {#if items.length === 0}
    <ViewState kind="empty" title={q ? 'Nothing matches' : 'The library is empty'}>
      {q ? `No movie or show matches “${q}”.` : 'Filed movies and shows show up here.'}
    </ViewState>
  {:else}
    <p class="meta">{plural(items.length, 'title')}{catalogue.hasNextPage ? ' so far' : ''}</p>
    <ul class="lib-grid" aria-label="Catalogue">
      {#each items as item (`${item.kind}/${item.id}`)}
        <li class="lib-tile">
          <a
            class="lib-tile__link"
            href={p('/apps/media/item/:kind/:id', { params: { kind: item.kind, id: item.id } })}
          >
            <span class="lib-tile__title">{item.title}</span>
            <span class="lib-tile__meta">
              <Badge tone="neutral">{item.kind === 'show' ? 'Show' : 'Movie'}</Badge>
              {#if item.year}
                <span class="meta">{item.year}</span>
              {/if}
            </span>
          </a>
        </li>
      {/each}
    </ul>
    {#if catalogue.hasNextPage}
      <Button variant="secondary" pending={catalogue.isFetchingNextPage} onclick={onMore}>Show more</Button>
    {/if}
  {/if}
{/if}
