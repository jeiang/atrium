<script lang="ts">
  import { route } from '../../shell/router';
  import ChatView from './ChatView.svelte';

  const id = $derived(route.params.id === undefined ? undefined : Number(route.params.id));
</script>

{#key id}
  <ChatView {id} />
{/key}
