<script lang="ts">
  import type { Snippet } from 'svelte';
  import type { Tone } from './Badge.svelte';
  import Icon from './Icon.svelte';
  import './kit.css';

  let { tone = 'neutral', children }: { tone?: Tone; children: Snippet } = $props();
</script>

<span class="mark mark--{tone}">
  {#if tone === 'ok'}
    <Icon name="check" />
  {:else}
    <span class="mark__dot"></span>
  {/if}
  {@render children()}
</span>
