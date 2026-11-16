<script lang="ts" module>
  export type Tone = 'neutral' | 'accent' | 'ok' | 'warn' | 'error' | 'info';
</script>

<script lang="ts">
  import type { Snippet } from 'svelte';
  import Icon from './Icon.svelte';
  import './kit.css';

  let { tone = 'neutral', children }: { tone?: Tone; children: Snippet } = $props();
</script>

<span class="tag tag--{tone}">
  {#if tone === 'ok'}
    <Icon name="check" />
  {/if}
  {@render children()}
</span>
