<script lang="ts">
  import type { Snippet } from 'svelte';
  import './kit.css';

  let { label, class: className, children }: { label?: string; class?: string; children: Snippet } = $props();
</script>

<ul class={['list', className]} aria-label={label}>
  {@render children()}
</ul>
