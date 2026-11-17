<script lang="ts">
  import Page from '../shared/ui/Page.svelte';
  import { p } from './router';
</script>

<Page title="Not found">
  <p>This page does not exist. <a class="btn btn--secondary" href={p('/')}>Go to Home</a></p>
</Page>
