<script lang="ts">
  import Field from '../../shared/ui/Field.svelte';

  let {
    label,
    help,
    disabled = false,
    value = $bindable(''),
  }: { label: string; help?: string; disabled?: boolean; value?: string } = $props();

  let shown = $state(false);

  function onToggle(): void {
    shown = !shown;
  }
</script>

<Field {label} {help} {value}>
  {#snippet control(
    c,
  )}
    <div class="secret">
      <input
        class="input secret__input"
        id={c.id}
        type={shown ? 'text' : 'password'}
        autocomplete="new-password"
        autocapitalize="off"
        spellcheck="false"
        {disabled}
        aria-describedby={c.describedby}
        bind:value
        onblur={c.onblur}
        oninput={c.oninput}
      >
      <button
        type="button"
        class="btn btn--secondary secret__toggle"
        aria-label={shown ? `Hide ${label}` : `Show ${label}`}
        aria-controls={c.id}
        {disabled}
        onclick={onToggle}
      >
        {shown ? 'Hide' : 'Show'}
      </button>
    </div>
  {/snippet}
</Field>
