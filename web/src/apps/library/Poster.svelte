<script lang="ts">
  import Button from '../../shared/ui/Button.svelte';
  import { type ArtKind, artMissing, artUrl } from './library';

  let { kind, id, title, lead = false }: { kind: ArtKind; id: string; title: string; lead?: boolean } = $props();

  let phase = $state<'loading' | 'shown' | 'checking' | 'none' | 'failed'>('loading');
  let attempt = $state(0);

  async function onError(): Promise<void> {
    phase = 'checking';
    phase = (await artMissing(kind, id)) ? 'none' : 'failed';
  }

  function onRetry(): void {
    phase = 'loading';
    attempt += 1;
  }
</script>

<div class="lib-poster" data-state={phase}>
  {#if phase === 'loading' || phase === 'shown'}
    {#key attempt}
      <img
        class="lib-poster__img"
        src={artUrl(kind, id)}
        alt=""
        width="200"
        height="300"
        decoding="async"
        loading={lead ? undefined : 'lazy'}
        fetchpriority={lead ? 'high' : undefined}
        onload={() => (phase = 'shown')}
        onerror={() => void onError()}
      >
    {/key}
  {:else}
    <span class="lib-poster__blank" aria-hidden="true">{title.slice(0, 1)}</span>
    {#if phase === 'failed'}
      <span class="lib-poster__retry">
        {#if lead}
          <Button size="sm" icon="undo" label="Retry the art for {title}" onclick={onRetry}>Retry</Button>
        {:else}
          <Button icon="undo" label="Retry the art for {title}" onclick={onRetry} />
        {/if}
      </span>
    {/if}
  {/if}
</div>
