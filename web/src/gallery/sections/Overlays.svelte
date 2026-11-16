<script lang="ts">
  import Button from '../../shared/ui/Button.svelte';
  import Dialog from '../../shared/ui/Dialog.svelte';
  import Section from '../../shared/ui/Section.svelte';
  import TextField from '../../shared/ui/TextField.svelte';
  import { toast } from '../../shared/ui/toast.svelte';

  let rename = $state(false);
  let confirm = $state(false);
  let sheet = $state(false);
  let deleting = $state(false);
  let title = $state('Plan the garden shed order');

  function onDelete(): void {
    deleting = true;
    window.setTimeout(() => {
      deleting = false;
      confirm = false;
      toast('Thread deleted', { action: { label: 'Undo', run: () => toast('Thread restored') } });
    }, 1500);
  }

  function onRename(event: SubmitEvent): void {
    event.preventDefault();
    rename = false;
    toast('Thread renamed');
  }
</script>

<Section id="g-overlays" data-gallery="overlays" title="Overlays">
  <div class="cluster">
    <Button onclick={() => (rename = true)}>Rename thread</Button>
    <Button variant="danger" onclick={() => (confirm = true)}>Delete thread</Button>
    <Button onclick={() => (sheet = true)}>Open sheet</Button>
    <Button onclick={() => toast('Reminder saved', { action: { label: 'View', run: () => {} } })}>Show toast</Button>
    <Button onclick={() => toast('The digest did not send', { tone: 'error' })}>Show error toast</Button>
  </div>
</Section>

<Dialog bind:open={rename} title="Rename thread">
  <form class="stack" id="rename-form" onsubmit={onRename}>
    <TextField label="Title" bind:value={title} required />
  </form>
  {#snippet actions()}
    <Button variant="quiet" onclick={() => (rename = false)}>Cancel</Button>
    <Button variant="primary" type="submit" form="rename-form">Rename</Button>
  {/snippet}
</Dialog>

<Dialog bind:open={confirm} title="Delete this thread?" pending={deleting} initialFocus="cancel">
  <p>The thread and its 14 messages are removed. Files stay in the library.</p>
  {#snippet actions()}
    <Button variant="quiet" data-cancel disabled={deleting} onclick={() => (confirm = false)}>Keep thread</Button>
    <Button variant="danger" pending={deleting} onclick={onDelete}>{deleting ? 'Deleting' : 'Delete'}</Button>
  {/snippet}
</Dialog>

<Dialog bind:open={sheet} title="Thread options" sheet>
  <div class="stack">
    <Button onclick={() => (sheet = false)}>Pin thread</Button>
    <Button onclick={() => (sheet = false)}>Mute for a day</Button>
  </div>
</Dialog>
