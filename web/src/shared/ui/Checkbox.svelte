<script lang="ts">
  import type { HTMLInputAttributes } from 'svelte/elements';
  import './kit.css';

  type Props = Omit<HTMLInputAttributes, 'type' | 'checked' | 'children'> & {
    label: string;
    help?: string;
    checked?: boolean;
  };

  let { label, help, checked = $bindable(false), ...rest }: Props = $props();
  const uid = $props.id();
</script>

<label class="check">
  <input {...rest} type="checkbox" bind:checked aria-describedby={help ? `${uid}-help` : undefined}>
  <span class="check__text">
    {label}
    {#if help}
      <span class="check__help" id="{uid}-help">{help}</span>
    {/if}
  </span>
</label>
