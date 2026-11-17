<script lang="ts">
  import { tick } from 'svelte';
  import { ApiError } from '../../contract';
  import Button from '../../shared/ui/Button.svelte';
  import Notice from '../../shared/ui/Notice.svelte';
  import TextField from '../../shared/ui/TextField.svelte';
  import { type AddSource, addProblem, handleAdd, type Problem } from './torrents.svelte';

  interface Props {
    offline?: boolean;
    onadded: (hash: string, shown: string) => void;
  }

  let { offline = false, onadded }: Props = $props();
  const uid = $props.id();

  let magnet = $state('');
  let refusal = $state<{ value: string; message: string }>();
  const fieldError = $derived(refusal?.value === magnet.trim() ? refusal.message : undefined);
  let problem = $state<Problem>();
  let sending = $state<'magnet' | 'file'>();
  let form = $state<HTMLFormElement>();
  let picker = $state<HTMLInputElement>();
  const reason = $derived(offline ? 'Cannot reach atrium' : undefined);

  async function focusMagnet(): Promise<void> {
    await tick();
    form?.querySelector<HTMLInputElement>('input[name="magnet"]')?.focus();
  }

  async function send(source: AddSource, shown: string): Promise<void> {
    sending = 'magnet' in source ? 'magnet' : 'file';
    refusal = undefined;
    problem = undefined;
    try {
      const hash = await handleAdd(source);
      if ('magnet' in source) magnet = '';
      onadded(hash, shown);
    } catch (error) {
      const refused = addProblem(error);
      if ('magnet' in source && error instanceof ApiError && error.status === 422) {
        refusal = { value: source.magnet, message: error.message };
        void focusMagnet();
      } else {
        problem = refused;
      }
    } finally {
      sending = undefined;
    }
  }

  function onSubmit(event: SubmitEvent): void {
    event.preventDefault();
    if (sending) return;
    const value = magnet.trim();
    if (value === '') {
      refusal = { value: '', message: 'Paste a magnet link or choose a .torrent file.' };
      problem = undefined;
      void focusMagnet();
      return;
    }
    void send({ magnet: value }, 'the magnet link');
  }

  function onPick(event: Event & { currentTarget: HTMLInputElement }): void {
    const file = event.currentTarget.files?.[0];
    event.currentTarget.value = '';
    if (!file || sending) return;
    void send({ file }, file.name);
  }
</script>

<section class="dl-add" aria-labelledby="{uid}-title">
  <h2 class="dl-add__title" id="{uid}-title">Add a torrent</h2>
  <form
    bind:this={form}
    class="dl-add__form"
    method="post"
    aria-labelledby="{uid}-title"
    novalidate
    onsubmit={onSubmit}
  >
    <div class="dl-add__field">
      <TextField
        label="Magnet link"
        name="magnet"
        bind:value={magnet}
        error={fieldError}
        autocomplete="off"
        autocapitalize="none"
        spellcheck="false"
        enterkeyhint="go"
        inputmode="url"
        readonly={sending !== undefined}
        {...{ autocorrect: 'off' }}
      />
    </div>
    <div class="dl-add__actions">
      <Button
        type="submit"
        variant="primary"
        pending={sending === 'magnet'}
        disabled={offline || sending === 'file'}
        {reason}
        >Add</Button
      >
      <Button
        pending={sending === 'file'}
        disabled={offline || sending === 'magnet'}
        {reason}
        onclick={() => picker?.click()}
        >Add a .torrent file</Button
      >
      <input
        bind:this={picker}
        class="visually-hidden"
        type="file"
        name="torrent"
        accept=".torrent,application/x-bittorrent"
        tabindex="-1"
        aria-hidden="true"
        onchange={onPick}
      >
    </div>
  </form>
  {#if problem}
    <Notice tone={problem.tone} variant="inline" title={problem.title}>{problem.text}</Notice>
  {/if}
</section>
