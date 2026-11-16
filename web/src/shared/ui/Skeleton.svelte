<script lang="ts">
  import './kit.css';

  let { lines = 3, label = 'Loading' }: { lines?: number; label?: string } = $props();
  const keys = $derived(Array.from({ length: lines }, (_, i) => i));
</script>

<div class="skel-group" aria-busy="true">
  <span class="visually-hidden">{label}</span>
  {#each keys as key (key)}
    <span class="skel" aria-hidden="true"></span>
  {/each}
</div>
