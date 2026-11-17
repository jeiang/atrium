<script lang="ts">
  import { useQueryClient } from '@tanstack/svelte-query';
  import type { KnowledgeRow } from '../../contract';
  import { formatDay, plural } from '../../shared/assistant/format';
  import { unreachable } from '../../shared/device.svelte';
  import Badge from '../../shared/ui/Badge.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import Checkbox from '../../shared/ui/Checkbox.svelte';
  import List from '../../shared/ui/List.svelte';
  import ListRow from '../../shared/ui/ListRow.svelte';
  import Segmented from '../../shared/ui/Segmented.svelte';
  import SelectField from '../../shared/ui/SelectField.svelte';
  import TextField from '../../shared/ui/TextField.svelte';
  import { toast } from '../../shared/ui/toast.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';
  import { p } from '../../shell/router';
  import {
    batchText,
    failureText,
    handleConfirmMatching,
    handleConfirmSelected,
    handleUndoEffects,
    type ReviewFilter,
    refreshKnowledge,
    useReview,
    writerLabel,
  } from './knowledge';

  const client = useQueryClient();
  const states = [
    { value: 'unconfirmed', label: 'Unconfirmed' },
    { value: 'imported', label: 'Imported' },
  ];
  const scopeOptions = [
    { value: '', label: 'Work and personal' },
    { value: 'work', label: 'Work' },
    { value: 'personal', label: 'Personal' },
  ];

  let shown = $state('unconfirmed');
  let text = $state('');
  let scope = $state('');
  let q = $state('');
  let picked = $state<number[]>([]);
  let busy = $state<'selected' | 'matching'>();

  $effect(() => {
    const next = text.trim();
    const timer = setTimeout(() => (q = next), 250);
    return () => clearTimeout(timer);
  });

  const filter = $derived({ state: shown, q, scope } as ReviewFilter);
  const list = useReview(() => filter);
  const rows = $derived(list.data?.pages.flatMap((page) => page.items) ?? []);
  const open = $derived(rows.filter((row) => !row.confirmed && !row.retracted));
  const chosen = $derived(picked.filter((id) => open.some((row) => row.id === id)));
  const filtered = $derived(q !== '' || scope !== '');
  const offline = $derived(unreachable(list));

  function meta(row: KnowledgeRow): string {
    const parts = [`Written by ${writerLabel(row.writer)}`, `edited ${formatDay(row.updated_at)}`];
    if (row.scope) parts.push(row.scope === 'work' ? 'Work' : 'Personal');
    return parts.join(' · ');
  }

  function toggle(id: number, on: boolean): void {
    picked = on ? [...picked.filter((other) => other !== id), id] : picked.filter((other) => other !== id);
  }

  async function undo(effects: readonly number[]): Promise<void> {
    try {
      await handleUndoEffects(effects);
      toast('Undone. They are unconfirmed again.');
    } catch (error) {
      toast(failureText(error), { tone: 'error' });
    } finally {
      await refreshKnowledge(client);
    }
  }

  async function run(kind: 'selected' | 'matching'): Promise<void> {
    if (busy) return;
    busy = kind;
    try {
      const batch = kind === 'selected' ? await handleConfirmSelected(chosen) : await handleConfirmMatching(filter);
      const effects = batch.effect_ids ?? [];
      picked = [];
      toast(
        batchText(batch, (count) => `Confirmed ${plural(count, 'record')}.`),
        {
          tone: batch.data.committed.length === 0 && batch.data.refused.length > 0 ? 'error' : undefined,
          action: effects.length > 0 ? { label: 'Undo', run: () => void undo(effects) } : undefined,
        },
      );
      await refreshKnowledge(client);
    } catch (error) {
      toast(failureText(error), { tone: 'error' });
    } finally {
      busy = undefined;
    }
  }

  function onMore(): void {
    void list.fetchNextPage();
  }

  function onRetry(): void {
    void list.refetch();
  }
</script>

<section class="kreview" aria-labelledby="kreview-title">
  <header class="kreview__head">
    <h1 class="page-title" id="kreview-title">Review</h1>
    <p class="kreview__lede">What the assistant or an import wrote that you have not confirmed yet.</p>
  </header>
  <div class="kreview__filters">
    <Segmented label="Show" options={states} bind:value={shown} />
    <TextField label="Find a record" type="search" bind:value={text} autocomplete="off" />
    <SelectField label="Scope" options={scopeOptions} bind:value={scope} />
  </div>
  <div class="kreview__bar">
    <Button
      variant="primary"
      icon="check"
      pending={busy === 'selected'}
      disabled={chosen.length === 0 || busy === 'matching'}
      reason="Select records to confirm them."
      onclick={() => run('selected')}
    >
      Confirm selected ({chosen.length})
    </Button>
    <Button
      pending={busy === 'matching'}
      disabled={open.length === 0 || busy === 'selected'}
      reason="Nothing here is waiting."
      onclick={() => run('matching')}
    >
      Confirm all that match
    </Button>
  </div>
  <div class="kreview__region" aria-busy={list.isFetching || undefined}>
    {#if offline}
      <ViewState kind="offline" title="Cannot reach atrium" level={2}>
        The list loads again when the connection returns.
        {#snippet action()}
          <Button onclick={onRetry}>Try again</Button>
        {/snippet}
      </ViewState>
    {:else if list.isPending}
      <ViewState kind="loading" title="Loading the review list" />
    {:else if list.isError && rows.length === 0}
      <ViewState kind="error" title="The review list could not be loaded" level={2}>
        {list.error.message}
        {#snippet action()}
          <Button onclick={onRetry}>Try again</Button>
        {/snippet}
      </ViewState>
    {:else if rows.length === 0}
      <ViewState kind="empty" title={filtered ? 'Nothing matches' : 'Nothing to review'} level={2}>
        {filtered
    ? 'Try other words, or show both scopes.'
    : 'New writes from the assistant and from imports wait here until you confirm them.'}
      </ViewState>
    {:else}
      <List label="Waiting for review">
        {#each rows as row (row.id)}
          <ListRow
            title={row.title || 'Untitled'}
            meta={meta(row)}
            href={p('/knowledge/:id', { params: { id: String(row.id) } })}
          >
            {#snippet end()}
              {#if row.retracted}
                <Badge tone="error">Retracted</Badge>
              {:else if row.confirmed}
                <Badge tone="ok">Confirmed</Badge>
              {:else}
                <Checkbox
                  label="Select"
                  aria-label="Select {row.title || 'Untitled'}"
                  checked={picked.includes(row.id)}
                  onchange={(event) => toggle(row.id, event.currentTarget.checked)}
                />
              {/if}
            {/snippet}
          </ListRow>
        {/each}
      </List>
      {#if list.hasNextPage}
        <Button class="subjects__more" pending={list.isFetchingNextPage} onclick={onMore}>Show more</Button>
      {/if}
      {#if list.isError}
        <p class="subjects__note" role="status">More records could not be loaded. {list.error.message}</p>
      {/if}
    {/if}
  </div>
</section>
