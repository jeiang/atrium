<script lang="ts">
  import type { Snippet } from 'svelte';
  import Icon from '../shared/ui/Icon.svelte';
  import Page from '../shared/ui/Page.svelte';

  let { title, legacy, children }: { title: string; legacy: string; children?: Snippet } = $props();
</script>

<Page {title}>
  {@render children?.()}
  <section class="placeholder" aria-label="Not built yet">
    <p class="placeholder__text">This screen is not in the new client yet.</p>
    <a class="btn btn--secondary" href={legacy}
      ><Icon name="external" /><span>Open {title} in the current interface</span></a
    >
  </section>
</Page>
