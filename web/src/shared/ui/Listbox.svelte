<script lang="ts" module>
  export interface ListOption {
    value: string;
    label: string;
    detail?: string;
    disabled?: boolean;
  }
</script>

<script lang="ts">
  import Icon from './Icon.svelte';
  import { isTypeaheadKey, typeahead } from './typeahead';
  import './widgets.css';

  interface Props {
    label: string;
    labelHidden?: boolean;
    options: readonly ListOption[];
    multiple?: boolean;
    value?: string;
    values?: string[];
    onchange?: (value: string) => void;
  }

  let {
    label,
    labelHidden = false,
    options,
    multiple = false,
    value = $bindable(),
    values = $bindable([]),
    onchange,
  }: Props = $props();

  const uid = $props.id();
  const find = typeahead();
  let list = $state<HTMLDivElement>();
  let active = $state(-1);

  const isSelected = (option: ListOption): boolean =>
    multiple ? values.includes(option.value) : value === option.value;

  function enabled(index: number): boolean {
    return options[index] !== undefined && !options[index].disabled;
  }

  function step(from: number, by: 1 | -1): number {
    for (let index = from + by; index >= 0 && index < options.length; index += by) {
      if (enabled(index)) return index;
    }
    return from;
  }

  const stop = $derived.by(() => {
    if (enabled(active)) return active;
    const chosen = options.findIndex((option) => isSelected(option) && !option.disabled);
    return chosen >= 0 ? chosen : step(-1, 1);
  });

  function moveTo(index: number): void {
    list?.querySelector<HTMLElement>(`#${CSS.escape(`${uid}-${index}`)}`)?.focus();
  }

  function pick(index: number): void {
    const option = options[index];
    if (!option || option.disabled) return;
    if (multiple) {
      values = values.includes(option.value) ? values.filter((v) => v !== option.value) : [...values, option.value];
    } else {
      value = option.value;
    }
    onchange?.(option.value);
  }

  function onKeydown(event: KeyboardEvent): void {
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
      const next = step(stop, event.key === 'ArrowDown' ? 1 : -1);
      if (multiple && event.shiftKey && next !== stop) pick(next);
      moveTo(next);
    } else if (event.key === 'Home') moveTo(step(-1, 1));
    else if (event.key === 'End') moveTo(step(options.length, -1));
    else if (event.key === 'Enter' || event.key === ' ') pick(stop);
    else if (multiple && event.key.toLowerCase() === 'a' && (event.ctrlKey || event.metaKey)) {
      const all = options.filter((option) => !option.disabled).map((option) => option.value);
      values = all.every((v) => values.includes(v)) ? [] : all;
    } else if (isTypeaheadKey(event)) {
      const labels = options.map((option) => (option.disabled ? '' : option.label));
      const match = find(event.key, labels, stop);
      if (match >= 0) moveTo(match);
    } else return;
    event.preventDefault();
  }
</script>

<div class="stack">
  <span class={labelHidden ? 'visually-hidden' : 'lbox-label'} id="{uid}-label">{label}</span>
  <div
    bind:this={list}
    class="lbox"
    role="listbox"
    aria-labelledby="{uid}-label"
    aria-multiselectable={multiple || undefined}
  >
    {#each options as option, index (option.value)}
      <div
        id="{uid}-{index}"
        class="opt"
        role="option"
        tabindex={index === stop ? 0 : -1}
        aria-selected={isSelected(option)}
        aria-disabled={option.disabled || undefined}
        onfocus={() => {
    if (enabled(index)) active = index;
  }}
        onkeydown={onKeydown}
        onclick={() => pick(index)}
      >
        <span class="opt__text">
          <span class="opt__label">{option.label}</span>
          {#if option.detail}
            <span class="opt__detail">{option.detail}</span>
          {/if}
        </span>
        <span class="opt__check"><Icon name="check" /></span>
      </div>
    {/each}
  </div>
</div>
