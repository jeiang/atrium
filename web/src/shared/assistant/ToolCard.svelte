<script lang="ts" module>
  const findable = typeof document !== 'undefined' && 'onbeforematch' in document.body;
</script>

<script lang="ts">
  import type { Snippet } from 'svelte';
  import { on } from 'svelte/events';
  import Icon, { type IconName } from '../ui/Icon.svelte';
  import { plural, webHref } from './format';
  import type { ToolCall } from './types';
  import './assistant.css';

  interface Props {
    call: ToolCall;
    expanded?: boolean;
    approval?: Snippet;
  }

  let { call, expanded = $bindable(false), approval }: Props = $props();

  const uid = $props.id();
  let body = $state<HTMLElement>();

  const icons: Record<ToolCall['state'], IconName> = {
    running: 'clock',
    done: 'check',
    failed: 'error',
    waiting: 'alert',
  };
  const states: Record<ToolCall['state'], string> = {
    running: 'Running',
    done: 'Done',
    failed: 'Failed',
    waiting: 'Waiting for you',
  };
  const line = $derived.by(() => {
    if (call.state === 'running') return call.progress || 'Working';
    if (call.state === 'waiting') return 'Paused until you decide below';
    const count = call.results?.length ?? 0;
    const base = call.preview || (call.state === 'failed' ? 'The tool did not finish' : 'Finished');
    return count > 0 ? `${base} · ${plural(count, 'source')}` : base;
  });

  const sources = $derived(
    (call.results ?? []).map((result) => {
      const href = webHref(result.url);
      return { text: result.title || result.url, href, host: href ? new URL(href).hostname : '' };
    }),
  );

  $effect(() => {
    if (!body || !findable) return;
    return on(body, 'beforematch', () => (expanded = true));
  });
</script>

<div class="card tool" data-tool={call.id} data-state={call.state}>
  <button
    type="button"
    class="tool__toggle"
    aria-expanded={expanded}
    aria-controls="{uid}-body"
    onclick={() => (expanded = !expanded)}
  >
    <span class="tool__mark"
      ><Icon name={icons[call.state]} /><span class="visually-hidden">{states[call.state]}</span></span
    >
    <span class="tool__text">
      <span class="tool__name">{call.name}</span>
      <span class="tool__line" aria-live={call.state === 'running' ? 'polite' : undefined}>{line}</span>
    </span>
    <Icon name="chevron" />
  </button>
  <div
    class="tool__body"
    id="{uid}-body"
    bind:this={body}
    hidden={expanded ? undefined : findable ? 'until-found' : true}
  >
    {#if call.args}
      <pre class="tool__args">{call.args}</pre>
    {/if}
    {#if sources.length > 0}
      <ul class="sources" aria-label="Sources">
        {#each sources as source, index (index)}
          <li class="sources__item">
            {#if source.href}
              <a class="link" href={source.href} target="_blank" rel="noopener noreferrer">
                {source.text}
                <small>{source.host}</small>
              </a>
            {:else}
              <span>{source.text}</span>
            {/if}
          </li>
        {/each}
      </ul>
    {/if}
  </div>
  {#if call.state === 'waiting' && approval}
    <div class="tool__embed">{@render approval()}</div>
  {/if}
</div>
