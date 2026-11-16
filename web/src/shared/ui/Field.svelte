<script lang="ts" module>
  export interface FieldControl {
    id: string;
    describedby: string | undefined;
    invalid: boolean;
    onblur: () => void;
    oninput: (event: Event & { currentTarget: { value: string } }) => void;
  }

  export interface FieldProps {
    label: string;
    help?: string;
    required?: boolean;
    status?: 'saving' | 'saved';
    error?: string;
    validate?: (value: string) => string | undefined;
  }
</script>

<script lang="ts">
  import type { Snippet } from 'svelte';
  import { announce } from '../announce.svelte';
  import Icon from './Icon.svelte';
  import Spinner from './Spinner.svelte';
  import './kit.css';

  interface Props extends FieldProps {
    value: string;
    control: Snippet<[FieldControl]>;
  }

  let { label, help, required = false, status, error, validate, value, control }: Props = $props();

  const uid = $props.id();
  const id = `${uid}-control`;
  let failing = $state(false);
  const own = $derived(failing ? validate?.(value) : undefined);
  const message = $derived(error ?? own);
  const describedby = $derived(
    [help && `${uid}-help`, message && `${uid}-error`, status && `${uid}-status`].filter(Boolean).join(' ') ||
      undefined,
  );

  function onblur(): void {
    const next = validate?.(value);
    const wasShowing = failing;
    failing = next !== undefined;
    if (next !== undefined && !wasShowing) announce(next);
  }

  function oninput(event: Event & { currentTarget: { value: string } }): void {
    if (failing && validate?.(event.currentTarget.value) === undefined) failing = false;
  }
</script>

<div class="field">
  <div class="field__label">
    <label for={id}>{label}</label>
    {#if required}
      <span class="field__req" aria-hidden="true">Required</span>
    {/if}
    {#if status}
      <span class="field__status" id="{uid}-status" data-status={status}>
        {#if status === 'saving'}
          <Spinner />Saving
        {:else}
          <Icon name="check" />Saved
        {/if}
      </span>
    {/if}
  </div>
  {@render control({ id, describedby, invalid: message !== undefined, onblur, oninput })}
  {#if help}
    <p class="field__help" id="{uid}-help">{help}</p>
  {/if}
  {#if message}
    <p class="field__error" id="{uid}-error"><Icon name="alert" />{message}</p>
  {/if}
</div>
