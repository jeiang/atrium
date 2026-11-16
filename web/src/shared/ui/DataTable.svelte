<script lang="ts" module>
  export interface Column {
    key: string;
    label: string;
    numeric?: boolean;
    sortable?: boolean;
  }

  export interface Sort {
    key: string;
    direction: 'ascending' | 'descending';
  }
</script>

<script lang="ts" generics="Row">
  import type { Snippet } from 'svelte';
  import Icon from './Icon.svelte';
  import Skeleton from './Skeleton.svelte';
  import './kit.css';

  interface Props {
    caption: string;
    columns: readonly Column[];
    rows: readonly Row[];
    key: (row: Row) => string;
    cell: Snippet<[Row, Column]>;
    sort?: Sort;
    current?: string;
    loading?: boolean;
    empty?: string;
    onopen?: (row: Row) => void;
  }

  let {
    caption,
    columns,
    rows,
    key,
    cell,
    sort = $bindable(),
    current = $bindable(),
    loading = false,
    empty = 'Nothing here',
    onopen,
  }: Props = $props();

  let body = $state<HTMLTableSectionElement>();
  const focusKey = $derived(rows.some((row) => key(row) === current) ? current : rows[0] && key(rows[0]));
  const first = $derived(columns[0]);
  const others = $derived(columns.slice(1));

  function onSort(column: Column): void {
    sort = {
      key: column.key,
      direction: sort?.key === column.key && sort.direction === 'ascending' ? 'descending' : 'ascending',
    };
  }

  function onOpen(row: Row): void {
    current = key(row);
    onopen?.(row);
  }

  function onRowKeydown(event: KeyboardEvent, index: number): void {
    const last = rows.length - 1;
    const target = { ArrowDown: index + 1, ArrowUp: index - 1, Home: 0, End: last }[event.key];
    if (event.key === 'Enter') {
      event.preventDefault();
      const row = rows[index];
      if (row) onOpen(row);
      return;
    }
    if (target === undefined) return;
    event.preventDefault();
    body?.querySelectorAll<HTMLTableRowElement>('tr[data-key]')[Math.max(0, Math.min(last, target))]?.focus();
  }
</script>

<div class="dt-wrap">
  <table class="dtable">
    <caption class="visually-hidden">
      {caption}
    </caption>
    <thead>
      <tr>
        {#each columns as column (column.key)}
          <th
            scope="col"
            class={{ num: column.numeric }}
            aria-sort={sort?.key === column.key ? sort.direction : undefined}
          >
            {#if column.sortable}
              <button type="button" class="dt-sort" onclick={() => onSort(column)}>
                {column.label}
                <Icon name={sort?.key === column.key ? 'chevron' : 'sort'} />
              </button>
            {:else}
              {column.label}
            {/if}
          </th>
        {/each}
      </tr>
    </thead>
    <tbody bind:this={body} aria-busy={loading || undefined}>
      {#if loading}
        <tr>
          <td colspan={columns.length}><Skeleton lines={3} /></td>
        </tr>
      {:else}
        {#each rows as row, index (key(row))}
          <tr
            data-key={key(row)}
            tabindex={key(row) === focusKey ? 0 : -1}
            aria-current={key(row) === current || undefined}
            onclick={() => onOpen(row)}
            onkeydown={(event) => onRowKeydown(event, index)}
          >
            {#each columns as column (column.key)}
              <td class={{ num: column.numeric }}>{@render cell(row, column)}</td>
            {/each}
          </tr>
        {:else}
          <tr>
            <td class="dt-empty" colspan={columns.length}>{empty}</td>
          </tr>
        {/each}
      {/if}
    </tbody>
  </table>
</div>

<ul class="list dt-list" aria-label={caption} aria-busy={loading || undefined}>
  {#if loading}
    <li class="row">
      <div class="row__main"><Skeleton lines={3} /></div>
    </li>
  {:else}
    {#each rows as row (key(row))}
      <li class="row" data-selected={key(row) === current || undefined}>
        <button
          type="button"
          class="row__main"
          aria-current={key(row) === current || undefined}
          onclick={() => onOpen(row)}
        >
          {#if first}
            <span class="row__title">{@render cell(row, first)}</span>
          {/if}
          <span class="row__meta">
            {#each others as column, i (column.key)}
              {#if i > 0}
                <span aria-hidden="true"> · </span>
              {/if}
              {@render cell(row, column)}
            {/each}
          </span>
        </button>
      </li>
    {:else}
      <li class="row"><div class="row__main dt-empty">{empty}</div></li>
    {/each}
  {/if}
</ul>
