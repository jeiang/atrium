<script lang="ts">
  import type { HTMLSelectAttributes } from 'svelte/elements';
  import Field, { type FieldProps } from './Field.svelte';
  import Icon from './Icon.svelte';

  type Props = FieldProps &
    Omit<HTMLSelectAttributes, 'value' | 'id' | 'children'> & {
      value?: string;
      options: readonly { value: string; label: string }[];
      placeholder?: string;
    };

  let {
    label,
    help,
    required,
    status,
    error,
    validate,
    value = $bindable(''),
    options,
    placeholder,
    ...rest
  }: Props = $props();
</script>

<Field {label} {help} {required} {status} {error} {validate} {value}>
  {#snippet control(
    c,
  )}
    <span class="select-wrap">
      <select
        {...rest}
        class="select"
        id={c.id}
        {required}
        aria-describedby={c.describedby}
        aria-invalid={c.invalid || undefined}
        bind:value
        onblur={c.onblur}
        onchange={c.oninput}
      >
        {#if placeholder !== undefined}
          <option value="" disabled>{placeholder}</option>
        {/if}
        {#each options as option (option.value)}
          <option value={option.value}>{option.label}</option>
        {/each}
      </select>
      <Icon name="chevron" />
    </span>
  {/snippet}
</Field>
