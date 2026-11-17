<script lang="ts">
  import { createQuery } from '@tanstack/svelte-query';
  import { assistantThreadHistory, type HistoryLine, ok } from '../../contract';
  import Button from '../../shared/ui/Button.svelte';
  import Icon from '../../shared/ui/Icon.svelte';
  import Spinner from '../../shared/ui/Spinner.svelte';
  import HistoryNode from './HistoryNode.svelte';
  import { failureText } from './manage';

  let { thread, line }: { thread: number; line: HistoryLine } = $props();

  let open = $state(false);
  const uid = $props.id();

  const zoom = createQuery(() => ({
    queryKey: ['thread', thread, 'history', line.from, line.n],
    queryFn: async ({ signal }) =>
      ok(await assistantThreadHistory(thread, { start: line.from, n: line.n }, { signal })),
    enabled: open,
    meta: { topics: ['assistant'] },
  }));

  const range = $derived(line.n === 1 ? `Message ${line.from}` : `Messages ${line.from} to ${line.to}`);

  function onToggle(): void {
    open = !open;
  }

  function onRetry(): void {
    void zoom.refetch();
  }
</script>

<li class="history__line">
  <button type="button" class="history__toggle" aria-expanded={open} aria-controls="{uid}-zoom" onclick={onToggle}>
    <Icon name="chevron" />
    <span class="history__range">{range}</span>
    <span class="history__text">{line.text}</span>
  </button>
  <div class="history__zoom" id="{uid}-zoom" hidden={!open}>
    {#if open}
      {#if zoom.isPending}
        <p class="history__state"><Spinner /> Loading</p>
      {:else if zoom.isError}
        <p class="history__state" role="alert">
          {failureText(zoom.error)}
          <Button size="sm" onclick={onRetry}>Try again</Button>
        </p>
      {:else if zoom.data.leaf != null}
        <p class="history__leaf">{zoom.data.leaf}</p>
      {:else}
        <ul class="history__lines">
          {#each zoom.data.lines as child (`${child.from}:${child.n}`)}
            <HistoryNode {thread} line={child} />
          {/each}
        </ul>
      {/if}
    {/if}
  </div>
</li>
