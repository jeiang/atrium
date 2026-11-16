<script lang="ts">
  import './kit.css';

  interface Props {
    label: string;
    options: readonly { value: string; label: string }[];
    value?: string;
  }

  let { label, options, value = $bindable('') }: Props = $props();
  const uid = $props.id();
</script>

<fieldset class="seg">
  <legend class="visually-hidden">{label}</legend>
  {#each options as option (option.value)}
    <label class="seg__opt">
      <input type="radio" name={uid} value={option.value} bind:group={value}>
      {option.label}
    </label>
  {/each}
</fieldset>
