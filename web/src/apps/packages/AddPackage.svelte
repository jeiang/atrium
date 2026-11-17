<script lang="ts">
  import { tick } from 'svelte';
  import { ApiError } from '../../contract';
  import Button from '../../shared/ui/Button.svelte';
  import Notice from '../../shared/ui/Notice.svelte';
  import TextField from '../../shared/ui/TextField.svelte';
  import { handleAdd, type Problem, problemOf } from './packages.svelte';

  interface Props {
    offline?: boolean;
    onadded: (id: number, number: string) => void;
  }

  let { offline = false, onadded }: Props = $props();
  const uid = $props.id();

  let number = $state('');
  let carrier = $state('');
  let merchant = $state('');
  let refusal = $state<{ value: string; message: string }>();
  const fieldError = $derived(refusal?.value === number.trim() ? refusal.message : undefined);
  let problem = $state<Problem>();
  let sending = $state(false);
  let form = $state<HTMLFormElement>();
  const reason = $derived(offline ? 'Cannot reach atrium' : undefined);

  async function focusNumber(): Promise<void> {
    await tick();
    form?.querySelector<HTMLInputElement>('input[name="tracking_number"]')?.focus();
  }

  async function send(value: string): Promise<void> {
    sending = true;
    refusal = undefined;
    problem = undefined;
    try {
      const id = await handleAdd({
        tracking_number: value,
        ...(carrier.trim() ? { carrier: carrier.trim() } : {}),
        ...(merchant.trim() ? { merchant: merchant.trim() } : {}),
      });
      number = '';
      carrier = '';
      merchant = '';
      onadded(id, value);
    } catch (error) {
      if (error instanceof ApiError && (error.status === 409 || error.status === 422)) {
        refusal = { value, message: error.message };
        void focusNumber();
      } else {
        problem = problemOf(error);
      }
    } finally {
      sending = false;
    }
  }

  function onSubmit(event: SubmitEvent): void {
    event.preventDefault();
    if (sending) return;
    const value = number.trim();
    if (value === '') {
      refusal = { value: '', message: 'Enter the tracking number from the shipping notice.' };
      problem = undefined;
      void focusNumber();
      return;
    }
    void send(value);
  }
</script>

<section class="pk-add" aria-labelledby="{uid}-title">
  <h2 class="pk-add__title" id="{uid}-title">Add a package</h2>
  <form
    bind:this={form}
    class="pk-add__form"
    method="post"
    aria-labelledby="{uid}-title"
    novalidate
    onsubmit={onSubmit}
  >
    <div class="pk-add__number">
      <TextField
        label="Tracking number"
        name="tracking_number"
        bind:value={number}
        error={fieldError}
        autocomplete="off"
        autocapitalize="characters"
        spellcheck="false"
        enterkeyhint="go"
        readonly={sending}
        {...{ autocorrect: 'off' }}
      />
    </div>
    <TextField
      label="Carrier"
      name="carrier"
      help="Optional"
      bind:value={carrier}
      autocomplete="off"
      readonly={sending}
    />
    <TextField
      label="Shop"
      name="merchant"
      help="Optional"
      bind:value={merchant}
      autocomplete="off"
      readonly={sending}
    />
    <div class="pk-add__actions">
      <Button type="submit" variant="primary" pending={sending} disabled={offline} {reason}>Add</Button>
    </div>
  </form>
  {#if problem}
    <Notice tone={problem.tone} variant="inline" title={problem.title}>{problem.text}</Notice>
  {/if}
</section>
