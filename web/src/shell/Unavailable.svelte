<script lang="ts">
  import { device } from '../shared/device.svelte';
  import Page from '../shared/ui/Page.svelte';
</script>

<Page title="Not available">
  <p>
    {device.online ? 'This page could not load.' : 'This page is not saved on this device.'}
    <button type="button" class="btn btn--secondary" onclick={() => location.reload()}>Try again</button>
  </p>
</Page>
