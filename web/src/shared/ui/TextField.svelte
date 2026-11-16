<script lang="ts">
  import type { HTMLInputAttributes } from 'svelte/elements';
  import Field, { type FieldProps } from './Field.svelte';

  type Props = FieldProps & Omit<HTMLInputAttributes, 'value' | 'id' | 'children'> & { value?: string };

  let { label, help, required, status, error, validate, value = $bindable(''), ...rest }: Props = $props();
</script>

<Field {label} {help} {required} {status} {error} {validate} {value}>
  {#snippet control(
    c,
  )}
    <input
      {...rest}
      class="input"
      id={c.id}
      {required}
      aria-describedby={c.describedby}
      aria-invalid={c.invalid || undefined}
      bind:value
      onblur={c.onblur}
      oninput={c.oninput}
    >
  {/snippet}
</Field>
