<script lang="ts">
  import { parseInboxId } from '../../shared/assistant/inboxId';
  import Page from '../../shared/ui/Page.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';
  import { isActive, navigate, p, route } from '../../shell/router';

  const target = $derived(parseInboxId(`${isActive.startsWith('/drafts') ? 'draft' : 'proposal'}:${route.params.id}`));

  $effect(() => {
    if (target) void navigate('/inbox/:item', { params: { item: `${target.type}:${target.id}` }, replace: true });
  });
</script>

{#if !target}
  <Page title="Inbox">
    <ViewState kind="empty" title="Nothing waits here">
      This address does not name an item atrium knows.
      {#snippet action()}
        <a class="btn btn--secondary" href={p('/inbox')}>Back to the Inbox</a>
      {/snippet}
    </ViewState>
  </Page>
{/if}
