<script lang="ts">
  import { useQueryClient } from '@tanstack/svelte-query';
  import Button from '../../shared/ui/Button.svelte';
  import Checkbox from '../../shared/ui/Checkbox.svelte';
  import Dialog from '../../shared/ui/Dialog.svelte';
  import Notice from '../../shared/ui/Notice.svelte';
  import SelectField from '../../shared/ui/SelectField.svelte';
  import TextField from '../../shared/ui/TextField.svelte';
  import { toast } from '../../shared/ui/toast.svelte';
  import { navigate } from '../../shell/router';
  import { EFFORTS, failureText, handleCreateSpace, handleRenameSpace, refreshKnowledge } from './knowledge';

  interface Props {
    open: boolean;
    phone: boolean;
    space?: { id: number; name: string };
  }

  let { open = $bindable(), phone, space }: Props = $props();

  const client = useQueryClient();
  const uid = $props.id();
  let name = $state('');
  let effort = $state('xhigh');
  let hidden = $state(false);
  let pending = $state(false);
  let error = $state<string>();
  let missing = $state(false);

  $effect(() => {
    if (!open) return;
    name = space?.name ?? '';
    effort = 'xhigh';
    hidden = false;
    error = undefined;
    missing = false;
  });

  async function handleSubmit(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    if (pending) return;
    const trimmed = name.trim();
    if (!trimmed) {
      missing = true;
      return;
    }
    pending = true;
    error = undefined;
    try {
      if (space) {
        await handleRenameSpace(space.id, trimmed);
        open = false;
        toast(`Renamed to ${trimmed}.`);
      } else {
        const created = await handleCreateSpace({ name: trimmed, effort, private: hidden });
        open = false;
        toast(`Made the space ${trimmed}.`);
        void navigate('/knowledge/spaces/:space', { params: { space: String(created) } });
      }
      await refreshKnowledge(client);
    } catch (failure) {
      error = failureText(failure);
    } finally {
      pending = false;
    }
  }

  function onCancel(): void {
    open = false;
  }
</script>

<Dialog bind:open title={space ? `Rename ${space.name}` : 'New space'} sheet={phone} {pending}>
  <form class="klink-form" id="{uid}-form" onsubmit={handleSubmit}>
    <TextField
      label="Name"
      bind:value={name}
      autocomplete="off"
      error={missing ? 'Give the space a name.' : undefined}
    />
    {#if !space}
      <SelectField label="Effort for chats in this space" options={EFFORTS} bind:value={effort} />
      <Checkbox label="Private" help="What belongs to this space stays inside it." bind:checked={hidden} />
    {/if}
    {#if error}
      <Notice tone="error" variant="inline">{error}</Notice>
    {/if}
  </form>
  {#snippet actions()}
    <Button variant="quiet" data-cancel disabled={pending} onclick={onCancel}>Cancel</Button>
    <Button type="submit" form="{uid}-form" variant="primary" {pending}>{space ? 'Rename' : 'Make the space'}</Button>
  {/snippet}
</Dialog>
