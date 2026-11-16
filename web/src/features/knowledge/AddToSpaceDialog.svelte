<script lang="ts">
  import { useQueryClient } from '@tanstack/svelte-query';
  import type { KnowledgeLinkRequestRelation } from '../../contract';
  import Button from '../../shared/ui/Button.svelte';
  import Combobox from '../../shared/ui/Combobox.svelte';
  import Dialog from '../../shared/ui/Dialog.svelte';
  import type { ListOption } from '../../shared/ui/Listbox.svelte';
  import Notice from '../../shared/ui/Notice.svelte';
  import SelectField from '../../shared/ui/SelectField.svelte';
  import { toast } from '../../shared/ui/toast.svelte';
  import {
    failureText,
    findSubjects,
    handleAddToSpace,
    handleUndoEffects,
    RELATIONS,
    refreshKnowledge,
  } from './knowledge';

  interface Props {
    open: boolean;
    space: { id: number; name: string };
    phone: boolean;
  }

  let { open = $bindable(), space, phone }: Props = $props();

  const client = useQueryClient();
  const uid = $props.id();
  let target = $state<ListOption>();
  let relation = $state('part_of');
  let pending = $state(false);
  let error = $state<string>();
  let missing = $state(false);

  $effect(() => {
    if (!open) return;
    target = undefined;
    relation = 'part_of';
    error = undefined;
    missing = false;
  });

  function onPick(option: ListOption | undefined): void {
    target = option;
    if (option) missing = false;
  }

  async function handleUndo(effects: readonly number[], title: string): Promise<void> {
    try {
      await handleUndoEffects(effects);
      toast(`Took ${title} out of ${space.name}.`);
    } catch (failure) {
      toast(failureText(failure, 'Cannot reach atrium. Nothing was undone.'), { tone: 'error' });
    } finally {
      await refreshKnowledge(client);
    }
  }

  async function handleSubmit(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    if (pending) return;
    if (!target) {
      missing = true;
      return;
    }
    const title = target.label;
    pending = true;
    error = undefined;
    try {
      const change = await handleAddToSpace(Number(target.value), space.id, relation as KnowledgeLinkRequestRelation);
      const effects = change.effect_ids ?? [];
      open = false;
      toast(`Added ${title} to ${space.name}.`, {
        action: effects.length > 0 ? { label: 'Undo', run: () => void handleUndo(effects, title) } : undefined,
      });
      await refreshKnowledge(client);
    } catch (failure) {
      error = failureText(failure, 'Cannot reach atrium. Nothing was added.');
    } finally {
      pending = false;
    }
  }

  function onCancel(): void {
    open = false;
  }
</script>

<Dialog bind:open title="Add a subject to {space.name}" sheet={phone} {pending}>
  <form class="klink-form" id="{uid}-form" onsubmit={handleSubmit}>
    <Combobox
      label="Subject"
      help="Search knowledge by title."
      placeholder="Type a name"
      required
      search={findSubjects}
      selected={target}
      onchange={onPick}
      error={missing ? 'Pick a subject to add.' : undefined}
    />
    <SelectField label="How it belongs" options={RELATIONS} bind:value={relation} />
    {#if error}
      <Notice tone="error" variant="inline">{error}</Notice>
    {/if}
  </form>
  {#snippet actions()}
    <Button variant="quiet" data-cancel disabled={pending} onclick={onCancel}>Cancel</Button>
    <Button type="submit" form="{uid}-form" variant="primary" {pending}>Add to the space</Button>
  {/snippet}
</Dialog>
