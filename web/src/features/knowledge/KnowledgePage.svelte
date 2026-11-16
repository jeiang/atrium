<script lang="ts">
  import { on } from 'svelte/events';
  import Button from '../../shared/ui/Button.svelte';
  import Icon from '../../shared/ui/Icon.svelte';
  import Page from '../../shared/ui/Page.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';
  import { isActive, p, route } from '../../shell/router';
  import { useInstructions, useSubjects } from './knowledge';
  import SubjectList from './SubjectList.svelte';
  import SubjectPage from './SubjectPage.svelte';
  import './knowledge.css';

  let phone = $state(false);

  $effect(() => {
    const query = matchMedia('(width < 861px)');
    phone = query.matches;
    return on(query, 'change', () => (phone = query.matches));
  });

  const listing = $derived(isActive('/knowledge/subjects'));
  const standing = $derived(isActive('/knowledge/instructions'));
  const raw = $derived(route.params.id);
  const named = $derived(raw !== undefined && /^[1-9]\d*$/.test(raw) ? Number(raw) : undefined);
  const invalid = $derived(raw !== undefined && named === undefined);
  const opening = $derived(!listing && !standing && raw === undefined);

  const instructions = useInstructions();
  const first = useSubjects(() => ({ q: '', kind: '', scope: '' }));
  const firstId = $derived(
    first.data?.pages[0]?.items.find((row) => row.id !== instructions.data?.record.id)?.id ??
      instructions.data?.record.id,
  );
  const firstStatus = $derived(first.status);
  const instructionsStatus = $derived(instructions.status);
  const settled = $derived(firstStatus !== 'pending' && instructionsStatus !== 'pending' ? firstId : undefined);
  let opened = $state<number>();

  $effect(() => {
    if (!opening) opened = undefined;
    else if (opened === undefined && settled !== undefined) opened = settled;
  });

  const shown = $derived(standing ? instructions.data?.record.id : opening ? (opened ?? settled) : named);
  const resolving = $derived(
    (standing && instructionsStatus === 'pending') ||
      (opening && (firstStatus === 'pending' || instructionsStatus === 'pending')),
  );
  const unresolved = $derived(
    (standing && instructionsStatus === 'error') ||
      (opening && firstStatus === 'error' && instructionsStatus === 'error'),
  );
  const firstFetch = $derived(first.fetchStatus);
  const instructionsFetch = $derived(instructions.fetchStatus);
  const offline = $derived(
    shown === undefined && ((standing && instructionsFetch === 'paused') || (opening && firstFetch === 'paused')),
  );

  function onRetry(): void {
    void first.refetch();
    void instructions.refetch();
  }
</script>

{#snippet pane()}
  {#if shown === undefined}
    <h1 class="visually-hidden">Knowledge</h1>
  {/if}
  {#if invalid}
    <ViewState kind="empty" title="This page does not exist" level={2}>
      The address does not name a knowledge page. Pick a subject instead.
    </ViewState>
  {:else if shown !== undefined}
    <SubjectPage id={shown} {phone} standing={standing || shown === instructions.data?.record.id} />
  {:else if offline}
    <ViewState kind="offline" title="You are offline" level={2}>
      Knowledge loads when the connection returns.
      {#snippet action()}
        <Button onclick={onRetry}>Try again</Button>
      {/snippet}
    </ViewState>
  {:else if resolving}
    <div class="kpage-loading"><ViewState kind="loading" title="Loading knowledge" /></div>
  {:else if unresolved}
    <ViewState kind="error" title="Knowledge could not be loaded" level={2}>
      {(first.error ?? instructions.error)?.message}
      {#snippet action()}
        <Button onclick={onRetry}>Try again</Button>
      {/snippet}
    </ViewState>
  {:else}
    <ViewState kind="empty" title="Nothing saved yet" level={2}>
      Pages, facts and lessons appear here when you or the assistant save them.
    </ViewState>
  {/if}
{/snippet}

{#if listing}
  <Page title="Knowledge" lede="Everything you and the assistant keep, newest first.">
    <SubjectList selected={shown} level={2} />
  </Page>
{:else if phone}
  <div class="knowledge knowledge--phone">
    <a class="knowledge__all" href={p('/knowledge/subjects')}><Icon name="knowledge" />All subjects</a>
    {@render pane()}
  </div>
{:else}
  <div class="knowledge">
    <nav class="knowledge__nav" aria-label="Knowledge subjects">
      <SubjectList selected={shown} {standing} level={2} />
    </nav>
    <div class="knowledge__pane">
      {@render pane()}
    </div>
  </div>
{/if}
