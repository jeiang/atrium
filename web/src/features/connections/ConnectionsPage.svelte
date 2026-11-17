<script lang="ts">
  import type { ConnectionView } from '../../contract';
  import { plural } from '../../shared/assistant/format';
  import { unreachable } from '../../shared/device.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import DataTable, { type Column } from '../../shared/ui/DataTable.svelte';
  import Page from '../../shared/ui/Page.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';
  import { navigate, p } from '../../shell/router';
  import ConnectionState from './ConnectionState.svelte';
  import { GOOGLE_ID, ownerText, reasonText, splitId, useConnections, uses, when } from './connections';
  import './connections.css';

  const list = useConnections();
  const rows = $derived(list.data ?? []);
  const offline = $derived(unreachable(list));
  const attention = $derived(rows.filter((row) => !['ok', 'untested', 'disabled'].includes(row.state)).length);
  const lede = $derived(
    list.data === undefined
      ? undefined
      : attention === 0
        ? `${plural(rows.length, 'connection')}, none failing`
        : `${plural(rows.length, 'connection')}, ${attention} need${attention === 1 ? 's' : ''} attention`,
  );

  const columns: readonly Column[] = [
    { key: 'name', label: 'Connection' },
    { key: 'state', label: 'State' },
    { key: 'owner', label: 'Owner' },
    { key: 'checked', label: 'Last checked' },
    { key: 'uses', label: 'Used for' },
  ];

  function onOpen(row: ConnectionView): void {
    void navigate('/settings/connections/:kind/:name', { params: splitId(row.id) });
  }

  function onRetry(): void {
    void list.refetch();
  }
</script>

{#snippet cell(
    row: ConnectionView,
    column: Column,
  )}
  {#if column.key === 'name'}
    <span class="conn-name">{row.label}</span>
    <span class="meta conn-kind">{row.kind}</span>
  {:else if column.key === 'state'}
    <span class="conn-status">
      <ConnectionState state={row.state} graceUntil={row.grace_until} />
      {#if reasonText(row)}
        <span class="meta conn-reason">{reasonText(row)}</span>
      {/if}
    </span>
  {:else if column.key === 'owner'}
    <span class="meta"><span class="conn-prefix">Owner </span>{ownerText(row)}</span>
  {:else if column.key === 'checked'}
    <span class="meta"><span class="conn-prefix">Checked </span>{when(row.checked_at)}</span>
  {:else}
    <span class="meta"><span class="conn-prefix">Used for </span>{uses(row)}</span>
  {/if}
{/snippet}

<Page title="Connections" eyebrow="Settings" {lede}>
  <a class="link conn-back" href={p('/settings')}>All settings</a>
  {#if offline}
    <ViewState kind="offline" title="Cannot reach atrium">
      Connections load when atrium answers again. Nothing was changed.
    </ViewState>
  {:else if list.isError && !list.data}
    <ViewState kind="error" title="Connections did not load">
      {list.error.message}
      {#snippet action()}
        <Button variant="secondary" onclick={onRetry}>Retry</Button>
      {/snippet}
    </ViewState>
  {:else if list.data && rows.length === 0}
    <ViewState kind="empty" title="No connections">
      atrium has no connection records yet. The host file and the apps declare them when they start.
    </ViewState>
  {:else}
    <DataTable
      caption="Connections"
      {columns}
      {rows}
      key={(row) => row.id}
      {cell}
      loading={list.isPending}
      empty="No connections"
      onopen={onOpen}
    />
  {/if}
  {#if list.data && !rows.some((row) => row.id === GOOGLE_ID)}
    <div class="cluster">
      <a class="btn btn--secondary" href={p('/settings/connections/:kind/:name', { params: splitId(GOOGLE_ID) })}
        >Set up Google</a
      >
    </div>
  {/if}
</Page>
