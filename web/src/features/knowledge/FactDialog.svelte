<script lang="ts">
  import { useQueryClient } from '@tanstack/svelte-query';
  import Button from '../../shared/ui/Button.svelte';
  import Checkbox from '../../shared/ui/Checkbox.svelte';
  import Dialog from '../../shared/ui/Dialog.svelte';
  import Notice from '../../shared/ui/Notice.svelte';
  import RadioGroup from '../../shared/ui/RadioGroup.svelte';
  import TextArea from '../../shared/ui/TextArea.svelte';
  import TextField from '../../shared/ui/TextField.svelte';
  import { toast } from '../../shared/ui/toast.svelte';
  import { navigate } from '../../shell/router';
  import { failureText, handleCreate, handleUndoEffects, refreshKnowledge } from './knowledge';

  interface Props {
    open: boolean;
    phone: boolean;
    pinned?: boolean;
  }

  let { open = $bindable(), phone, pinned = false }: Props = $props();

  const client = useQueryClient();
  const uid = $props.id();
  const scopes = [
    { value: 'personal', label: 'Personal' },
    { value: 'work', label: 'Work' },
  ] as const;

  let title = $state('');
  let body = $state('');
  let scope = $state('personal');
  let hidden = $state(false);
  let pending = $state(false);
  let error = $state<string>();
  let missingTitle = $state(false);
  let missingBody = $state(false);

  $effect(() => {
    if (!open) return;
    title = '';
    body = '';
    scope = 'personal';
    hidden = false;
    error = undefined;
    missingTitle = false;
    missingBody = false;
  });

  async function undo(effects: readonly number[], name: string): Promise<void> {
    try {
      await handleUndoEffects(effects);
      toast(`Retracted: ${name}. Kept in history.`);
    } catch (failure) {
      toast(failureText(failure), { tone: 'error' });
    } finally {
      await refreshKnowledge(client);
    }
  }

  async function handleSubmit(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    if (pending) return;
    const name = title.trim();
    const text = body.trim();
    missingTitle = !name;
    missingBody = !text;
    if (missingTitle || missingBody) return;
    pending = true;
    error = undefined;
    try {
      const change = await handleCreate({
        title: name,
        body: text,
        scope: scope === 'work' ? 'work' : 'personal',
        private: hidden,
        pinned,
      });
      open = false;
      const effects = change.effect_ids ?? [];
      toast(`Saved: ${name}.`, {
        action: effects.length > 0 ? { label: 'Undo', run: () => void undo(effects, name) } : undefined,
      });
      if (!pinned) void navigate('/knowledge/:id', { params: { id: String(change.data.id) } });
      await refreshKnowledge(client);
    } catch (failure) {
      error = failureText(failure);
    } finally {
      pending = false;
    }
  }
</script>

<Dialog bind:open title={pinned ? 'Add a fact about me' : 'New fact'} sheet={phone} {pending}>
  <form class="klink-form" id="{uid}-form" onsubmit={handleSubmit} novalidate>
    <TextField
      label="Title"
      bind:value={title}
      autocomplete="off"
      required
      error={missingTitle ? 'Give the fact a title.' : undefined}
    />
    <TextArea
      label="Text"
      bind:value={body}
      rows={5}
      required
      error={missingBody ? 'Write what the fact says.' : undefined}
    />
    <RadioGroup legend="Scope" options={scopes} bind:value={scope} />
    <Checkbox label="Private" help="Only chats that belong to it can read it." bind:checked={hidden} />
    {#if error}
      <Notice tone="error" variant="inline">{error}</Notice>
    {/if}
  </form>
  {#snippet actions()}
    <Button variant="quiet" data-cancel disabled={pending} onclick={() => (open = false)}>Cancel</Button>
    <Button type="submit" form="{uid}-form" variant="primary" {pending}>Save the fact</Button>
  {/snippet}
</Dialog>
