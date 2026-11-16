<script lang="ts">
  import Button from '../../shared/ui/Button.svelte';
  import Section from '../../shared/ui/Section.svelte';

  let saving = $state(false);
  let count = $state(0);

  function onSave(): void {
    saving = true;
    window.setTimeout(() => {
      saving = false;
      count++;
    }, 1200);
  }
</script>

<Section id="g-buttons" data-gallery="buttons" title="Buttons">
  <div class="cluster">
    <Button variant="primary" pending={saving} onclick={onSave}>{saving ? 'Saving' : 'Save'}</Button>
    <Button>Preview</Button>
    <Button variant="quiet">Skip</Button>
    <Button variant="danger">Delete note</Button>
    <Button variant="secondary" icon="plus" label="New thread" />
  </div>
  <div class="cluster">
    <Button size="sm">Small</Button>
    <Button size="sm" variant="primary" icon="check">Approve</Button>
    <Button disabled>Unavailable</Button>
  </div>
  <div class="stack">
    <Button variant="primary" disabled reason="Connect a mail account first">Send digest</Button>
  </div>
  <p class="meta" data-testid="save-count">Saved {count} times</p>
</Section>

<Section id="g-links" data-gallery="links" title="Links">
  <div class="cluster">
    <a class="link" href="#g-buttons">Back to buttons</a>
    <a class="link" href="#g-table">Jump to the table</a>
  </div>
</Section>
