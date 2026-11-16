<script lang="ts">
  import './kit.css';

  interface Props {
    legend: string;
    options: readonly { value: string; label: string; help?: string; disabled?: boolean }[];
    value?: string;
    disabled?: boolean;
  }

  let { legend, options, value = $bindable(''), disabled = false }: Props = $props();
  const uid = $props.id();
</script>

<fieldset class="choices" {disabled}>
  <legend class="field__label">{legend}</legend>
  {#each options as option (option.value)}
    <label class="check">
      <input
        type="radio"
        name={uid}
        value={option.value}
        disabled={option.disabled}
        bind:group={value}
        aria-describedby={option.help ? `${uid}-${option.value}-help` : undefined}
      >
      <span class="check__text">
        {option.label}
        {#if option.help}
          <span class="check__help" id="{uid}-{option.value}-help">{option.help}</span>
        {/if}
      </span>
    </label>
  {/each}
</fieldset>
