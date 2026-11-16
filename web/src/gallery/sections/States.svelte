<script lang="ts">
  import Button from '../../shared/ui/Button.svelte';
  import Section from '../../shared/ui/Section.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';
</script>

<Section id="g-states" data-gallery="states" title="View states">
  <div class="grid">
    <ViewState kind="empty" title="No threads yet">Start a thread from Today.</ViewState>
    <ViewState kind="loading" title="Loading threads" />
    <ViewState kind="error" title="Threads did not load">
      The server answered with an error.
      {#snippet action()}
        <Button>Try again</Button>
      {/snippet}
    </ViewState>
    <ViewState kind="offline" title="You are offline">Saved threads are still readable.</ViewState>
    <ViewState kind="blocked" title="Mail is not connected">
      Connect an account to see mail.
      {#snippet action()}
        <Button variant="primary">Connect</Button>
      {/snippet}
    </ViewState>
    <ViewState kind="pending" title="Importing contacts">This takes about a minute.</ViewState>
    <ViewState kind="partial" title="Some feeds did not load">2 of 5 feeds are missing.</ViewState>
  </div>
</Section>
