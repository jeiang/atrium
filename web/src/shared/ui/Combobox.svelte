<script lang="ts">
  import { on } from 'svelte/events';
  import { anchorTo } from './anchor';
  import Field from './Field.svelte';
  import Icon from './Icon.svelte';
  import type { ListOption } from './Listbox.svelte';
  import Spinner from './Spinner.svelte';
  import './widgets.css';

  interface Props {
    label: string;
    help?: string;
    required?: boolean;
    error?: string;
    placeholder?: string;
    options?: readonly ListOption[];
    search?: (query: string, signal: AbortSignal) => Promise<readonly ListOption[]>;
    selected?: ListOption;
    onchange?: (option: ListOption | undefined) => void;
  }

  let {
    label,
    help,
    required,
    error,
    placeholder,
    options = [],
    search,
    selected = $bindable(),
    onchange,
  }: Props = $props();

  const uid = $props.id();
  let root = $state<HTMLDivElement>();
  let input = $state<HTMLInputElement>();
  let popup = $state<HTMLDivElement>();
  let text = $state('');
  let reflected: ListOption | undefined;
  let expanded = $state(false);
  let active = $state(-1);
  let found = $state<readonly ListOption[]>([]);
  let status = $state<'idle' | 'loading' | 'error'>('idle');
  let pending: AbortController | undefined;
  let timer = 0;

  const query = $derived(selected && text === selected.label ? '' : text.trim().toLowerCase());
  const shown = $derived(
    search ? found : options.filter((option) => !query || option.label.toLowerCase().includes(query)),
  );

  $effect.pre(() => {
    if (selected === reflected) return;
    reflected = selected;
    text = selected?.label ?? '';
  });

  $effect(() => {
    if (!popup) return;
    const isOpen = popup.matches(':popover-open');
    if (expanded && !isOpen) popup.showPopover();
    else if (!expanded && isOpen) popup.hidePopover();
  });

  $effect(() => {
    if (!popup) return;
    const offDown = on(popup, 'mousedown', (event) => event.preventDefault());
    const offClick = on(popup, 'click', onPopupClick);
    return () => {
      offDown();
      offClick();
    };
  });

  $effect(() => {
    if (!expanded) return;
    return on(document, 'pointerdown', (event) => {
      if (!(event.target instanceof Node) || !root?.contains(event.target)) close();
    });
  });

  function close(): void {
    expanded = false;
    active = -1;
  }

  function step(from: number, by: 1 | -1): number {
    const count = shown.length;
    for (let offset = 1; offset <= count; offset++) {
      const index = (((from + by * offset) % count) + count) % count;
      if (!shown[index]?.disabled) return index;
    }
    return -1;
  }

  function moveTo(index: number): void {
    active = index;
    popup?.querySelector(`#${CSS.escape(`${uid}-opt-${index}`)}`)?.scrollIntoView({ block: 'nearest' });
  }

  function choose(index: number): void {
    const option = shown[index];
    if (!option || option.disabled) return;
    reflected = option;
    selected = option;
    text = option.label;
    close();
    onchange?.(option);
  }

  function clearSelection(): void {
    if (!selected) return;
    reflected = undefined;
    selected = undefined;
    onchange?.(undefined);
  }

  function runSearch(value: string): void {
    if (!search) return;
    pending?.abort();
    window.clearTimeout(timer);
    const q = value.trim();
    if (!q) {
      found = [];
      status = 'idle';
      close();
      return;
    }
    const controller = new AbortController();
    pending = controller;
    status = 'loading';
    timer = window.setTimeout(async () => {
      try {
        const results = await search(q, controller.signal);
        if (controller.signal.aborted) return;
        found = results;
        status = 'idle';
      } catch {
        if (!controller.signal.aborted) status = 'error';
      }
    }, 200);
  }

  function onInput(event: Event & { currentTarget: HTMLInputElement }): void {
    text = event.currentTarget.value;
    clearSelection();
    active = -1;
    expanded = search ? text.trim() !== '' : true;
    runSearch(text);
  }

  function onKeydown(event: KeyboardEvent): void {
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
      event.preventDefault();
      const down = event.key === 'ArrowDown';
      if (!expanded) {
        if (search && found.length === 0 && status === 'idle') return;
        expanded = true;
        if (event.altKey) return;
        moveTo(down ? step(-1, 1) : step(0, -1));
        return;
      }
      moveTo(down ? step(active, 1) : step(active < 0 ? 0 : active, -1));
    } else if (event.key === 'Enter') {
      if (event.isComposing || !expanded || active < 0) return;
      event.preventDefault();
      choose(active);
    } else if (event.key === 'Escape') {
      if (expanded) {
        event.preventDefault();
        close();
      } else if (text) {
        event.preventDefault();
        text = '';
        clearSelection();
        runSearch('');
      }
    }
  }

  function onBlur(event: FocusEvent): void {
    if (!(event.relatedTarget instanceof Node && popup?.contains(event.relatedTarget))) close();
  }

  function onPopupClick(event: MouseEvent): void {
    const target = event.target instanceof Element ? event.target.closest<HTMLElement>('[role="option"]') : null;
    if (target) choose(Number(target.dataset.index));
  }
</script>

<Field {label} {help} {required} {error} value={text}>
  {#snippet control(
    c,
  )}
    <div bind:this={root}>
      <input
        bind:this={input}
        class="input"
        id={c.id}
        type="text"
        role="combobox"
        autocomplete="off"
        spellcheck="false"
        {placeholder}
        {required}
        value={text}
        aria-describedby={c.describedby}
        aria-invalid={c.invalid || undefined}
        aria-autocomplete="list"
        aria-expanded={expanded}
        aria-controls="{uid}-popup"
        aria-activedescendant={expanded && active >= 0 ? `${uid}-opt-${active}` : undefined}
        oninput={(event) => {
    onInput(event);
    c.oninput(event);
  }}
        onkeydown={onKeydown}
        onblur={(event) => {
    onBlur(event);
    c.onblur();
  }}
      >
      <div
        bind:this={popup}
        {@attach anchorTo(() => input, true)}
        id="{uid}-popup"
        class="pop pop--match"
        popover="manual"
        aria-busy={status === 'loading' || undefined}
      >
        {#if expanded && status === 'loading'}
          <p class="pop__note" role="status"><Spinner />Searching</p>
        {:else if expanded && status === 'error'}
          <p class="pop__note" role="status">Search failed. Type again to retry.</p>
        {:else if expanded && shown.length === 0}
          <p class="pop__note" role="status">No matches</p>
        {:else if expanded}
          <div role="listbox" aria-label={label}>
            {#each shown as option, index (option.value)}
              <div
                id="{uid}-opt-{index}"
                class="opt"
                role="option"
                tabindex="-1"
                aria-selected={selected?.value === option.value}
                aria-disabled={option.disabled || undefined}
                data-index={index}
                data-active={index === active || undefined}
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
        {/if}
      </div>
    </div>
  {/snippet}
</Field>
