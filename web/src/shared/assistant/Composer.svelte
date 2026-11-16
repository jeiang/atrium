<script lang="ts">
  import type { Snippet } from 'svelte';
  import Button from '../ui/Button.svelte';

  interface Props {
    value?: string;
    label?: string;
    placeholder?: string;
    disabled?: boolean;
    busy?: boolean;
    running?: boolean;
    stopping?: boolean;
    scope?: Snippet;
    onsubmit: (text: string) => void | Promise<void>;
    onstop?: () => void | Promise<void>;
  }

  let {
    value = $bindable(''),
    label = 'Message',
    placeholder = 'Ask for something',
    disabled = false,
    busy = false,
    running = false,
    stopping = false,
    scope,
    onsubmit,
    onstop,
  }: Props = $props();

  const touchQuery = '(width < 861px), ((pointer: coarse) and (not (any-pointer: fine)))';
  const id = $props.id();
  let area = $state<HTMLTextAreaElement>();
  let touch = $state(false);
  let sending = $state(false);
  let failed = $state<string>();
  const grows = typeof CSS !== 'undefined' && CSS.supports('field-sizing', 'content');
  const empty = $derived(value.trim() === '');
  const blocked = $derived(disabled || empty || sending || busy);

  $effect(() => {
    const query = matchMedia(touchQuery);
    touch = query.matches;
    const update = (event: MediaQueryListEvent): void => {
      touch = event.matches;
    };
    query.addEventListener('change', update);
    return () => query.removeEventListener('change', update);
  });

  $effect(() => {
    void value;
    if (grows || !area) return;
    area.style.height = 'auto';
    area.style.height = `${area.scrollHeight + area.offsetHeight - area.clientHeight}px`;
  });

  async function handleSend(): Promise<void> {
    if (blocked) return;
    const text = value.trim();
    sending = true;
    failed = undefined;
    try {
      await onsubmit(text);
      value = '';
    } catch (error) {
      failed = error instanceof Error && error.message ? error.message : 'The message was not sent.';
    } finally {
      sending = false;
    }
  }

  function onSubmit(event: SubmitEvent): void {
    event.preventDefault();
    void handleSend();
  }

  function onKeydown(event: KeyboardEvent): void {
    if (event.key !== 'Enter' || event.shiftKey || touch) return;
    if (event.isComposing || event.keyCode === 229) return;
    event.preventDefault();
    void handleSend();
  }

  function onStop(): void {
    void onstop?.();
  }
</script>

<form class="composer" class:composer--scoped={scope !== undefined} onsubmit={onSubmit}>
  {#if scope}
    <div class="composer__scope">{@render scope()}</div>
  {/if}
  <label class="visually-hidden" for={id}>{label}</label>
  <textarea
    {id}
    class="composer__text"
    class:composer__text--fallback={!grows}
    bind:this={area}
    bind:value
    rows="1"
    {placeholder}
    enterkeyhint={touch ? 'enter' : 'send'}
    aria-invalid={failed ? true : undefined}
    aria-describedby={failed ? `${id}-error` : undefined}
    onkeydown={onKeydown}
  ></textarea>
  <div class="composer__actions">
    {#if onstop && running}
      <Button variant="secondary" pending={stopping} onclick={onStop}>Stop run</Button>
    {/if}
    {#if touch}
      <Button
        type="submit"
        variant="primary"
        icon="arrow"
        label="Send"
        disabled={blocked && !sending}
        pending={sending}
      />
    {:else}
      <Button type="submit" variant="primary" icon="arrow" disabled={blocked && !sending} pending={sending}
        >Send</Button
      >
    {/if}
  </div>
  {#if failed}
    <p class="composer__error" id="{id}-error" role="alert">
      <span>{failed}</span>
      <Button size="sm" onclick={handleSend}>Retry</Button>
    </p>
  {/if}
</form>

<style>
  .composer {
    display: grid;
    grid-template-columns: minmax(0, 1fr) auto;
    grid-template-areas:
      "text text"
      "scope actions"
      "error error";
    align-items: end;
    gap: var(--s-2);
    padding: var(--s-2) var(--s-2) var(--s-2) var(--s-3);
    background: var(--leaf);
    border: 1px solid var(--line);
    border-radius: var(--r-lg);
    box-shadow: var(--shadow-2);
  }

  .composer:focus-within {
    border-color: var(--line-strong);
  }

  .composer__scope {
    grid-area: scope;
    display: flex;
    align-items: center;
    min-width: 0;
  }

  .composer__text {
    grid-area: text;
    field-sizing: content;
    box-sizing: border-box;
    inline-size: 100%;
    min-block-size: 44px;
    max-block-size: 40vh;
    padding: var(--s-2) var(--s-1);
    font: inherit;
    font-size: var(--t-input);
    line-height: var(--lh-body);
    color: var(--ink);
    background: transparent;
    border: 0;
    border-radius: var(--r-sm);
    resize: none;
    overflow-y: auto;
  }

  .composer__text:focus-visible {
    outline-offset: 0;
  }

  .composer__text::placeholder {
    color: var(--ink-3);
  }

  .composer__actions {
    grid-area: actions;
    display: flex;
    gap: var(--s-2);
    justify-content: end;
  }

  .composer__error {
    grid-area: error;
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--s-2);
    margin: 0;
    color: var(--brick);
    font-size: var(--t-sm);
  }

  @media (--phone) {
    .composer {
      grid-template-columns: auto minmax(0, 1fr) auto;
      grid-template-areas:
        "text text actions"
        "error error error";
      row-gap: 0;
      column-gap: var(--s-1);
      padding: 2px 2px 2px var(--s-2);
    }

    .composer--scoped {
      grid-template-areas:
        "scope text actions"
        "error error error";
    }

    .composer__text {
      max-block-size: 9lh;
    }
  }
</style>
