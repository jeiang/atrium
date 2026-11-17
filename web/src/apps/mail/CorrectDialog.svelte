<script lang="ts">
  import { untrack } from 'svelte';
  import { ApiError } from '../../contract';
  import { device, refusedOffline } from '../../shared/device.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import Dialog from '../../shared/ui/Dialog.svelte';
  import Notice from '../../shared/ui/Notice.svelte';
  import SelectField from '../../shared/ui/SelectField.svelte';
  import Skeleton from '../../shared/ui/Skeleton.svelte';
  import { type Corrected, handleCorrect, type Problem, problemOf, unknownOutcome, useAccountFolders } from './mail';
  import { useStatus } from './manage';

  interface Props {
    message: number;
    account: string;
    subject: string;
    current?: string | null;
    open: boolean;
    ondone: (corrected: Corrected) => void;
    onunknown: () => void;
  }

  let { message, account, subject, current, open = $bindable(), ondone, onunknown }: Props = $props();

  const folders = useAccountFolders(
    () => account,
    () => open,
  );
  const status = useStatus(() => open);
  const options = $derived(
    (folders.data ?? [])
      .filter((item) => item.folder.toUpperCase() !== 'INBOX')
      .map((item) => ({ value: item.folder, label: item.folder })),
  );
  const live = $derived(status.data?.mode === 'live');

  let folder = $state('');
  let running = $state(false);
  let problem = $state<Problem>();

  $effect(() => {
    if (open)
      untrack(() => {
        folder = current ?? '';
        problem = undefined;
      });
  });

  async function onRun(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    if (running || !device.online || folder === '') return;
    running = true;
    problem = undefined;
    try {
      const corrected = await handleCorrect(message, folder);
      open = false;
      ondone(corrected);
    } catch (error) {
      if ((error instanceof ApiError && !unknownOutcome(error)) || refusedOffline(error)) problem = problemOf(error);
      else {
        open = false;
        onunknown();
      }
    } finally {
      running = false;
    }
  }
</script>

<Dialog bind:open title="Correct this decision" pending={running} initialFocus="first">
  <form id="mail-correct-{message}" class="stack" onsubmit={onRun}>
    <p class="mail-statement">{subject || 'No subject'}</p>
    {#if folders.isPending}
      <Skeleton lines={2} label="Loading the folders" />
    {:else if folders.isError}
      <Notice tone="error" variant="inline" title="The folders did not load">
        {folders.error.message}
        {#snippet action()}
          <Button variant="secondary" onclick={() => void folders.refetch()}>Retry</Button>
        {/snippet}
      </Notice>
    {:else}
      <SelectField
        label="Where should this go?"
        help={live
    ? 'Records the correction and moves the message to this folder.'
    : 'Records the correction so atrium learns from it. The message stays where it is.'}
        {options}
        placeholder="Choose a folder"
        required
        bind:value={folder}
      />
    {/if}
  </form>
  {#if problem}
    <Notice tone={problem.tone} variant="inline" title={problem.title}>{problem.text}</Notice>
  {/if}
  {#snippet actions()}
    <Button variant="quiet" data-cancel disabled={running} onclick={() => (open = false)}>Cancel</Button>
    <Button
      type="submit"
      form="mail-correct-{message}"
      variant="primary"
      pending={running}
      disabled={!device.online || folder === '' || !folders.isSuccess}
      reason={device.online ? undefined : 'Cannot reach atrium.'}
      >Correct</Button
    >
  {/snippet}
</Dialog>
