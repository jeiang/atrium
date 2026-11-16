<script lang="ts">
  import { announcement } from './announce.svelte';
</script>

<div class="visually-hidden" role="status" aria-live="polite" aria-atomic="true">{announcement.polite}</div>
<div class="visually-hidden" role="alert" aria-live="assertive" aria-atomic="true">{announcement.assertive}</div>
