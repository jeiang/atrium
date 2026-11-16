<script lang="ts">
  import type { HTMLTextareaAttributes } from 'svelte/elements';
  import Field, { type FieldProps } from './Field.svelte';

  type Props = FieldProps & Omit<HTMLTextareaAttributes, 'value' | 'id' | 'children'> & { value?: string };

  let { label, help, required, status, error, validate, value = $bindable(''), ...rest }: Props = $props();
</script>

<Field {label} {help} {required} {status} {error} {validate} {value}>
  {#snippet control(
    c,
  )}
    <textarea
      {...rest}
      class="textarea"
      id={c.id}
      {required}
      aria-describedby={c.describedby}
      aria-invalid={c.invalid || undefined}
      bind:value
      onblur={c.onblur}
      oninput={c.oninput}
    ></textarea>
  {/snippet}
</Field>
