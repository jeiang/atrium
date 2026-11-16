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
    findTargets,
    handleAddLink,
    handleUndoEffects,
    RELATIONS,
    refreshKnowledge,
    relationLabel,
  } from './knowledge';

  interface Props {
    open: boolean;
    id: number;
    title: string;
    phone: boolean;
  }

  let { open = $bindable(), id, title, phone }: Props = $props();

  const client = useQueryClient();
  let target = $state<ListOption>();
  let relation = $state<string>('see_also');
  let pending = $state(false);
  let error = $state<string>();
  let missingTarget = $state(false);

  $effect(() => {
    if (!open) return;
    target = undefined;
    relation = 'see_also';
    error = undefined;
    missingTarget = false;
  });

  function onPick(option: ListOption | undefined): void {
    target = option;
    if (option) missingTarget = false;
  }

  async function handleUndo(effects: readonly number[], name: string): Promise<void> {
    try {
      await handleUndoEffects(effects);
      toast(`Removed the link to ${name}.`);
    } catch (failure) {
      toast(failure instanceof Error ? failure.message : 'Undo failed.', { tone: 'error' });
    } finally {
      await refreshKnowledge(client);
    }
  }

  async function handleSubmit(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    if (pending) return;
    if (!target) {
      missingTarget = true;
      return;
    }
    const [kind, raw] = target.value.split(':');
    const name = target.label;
    pending = true;
    error = undefined;
    try {
      const change = await handleAddLink(
        id,
        { kind: kind === 'task' ? 'task' : 'record', id: raw ?? '' },
        relation as KnowledgeLinkRequestRelation,
      );
      const effects = change.effect_ids ?? [];
      open = false;
      toast(`Linked to ${name} as ${relationLabel(relation).toLowerCase()}.`, {
        action: effects.length > 0 ? { label: 'Undo', run: () => void handleUndo(effects, name) } : undefined,
      });
      await refreshKnowledge(client);
    } catch (failure) {
      error =
        failure instanceof TypeError
          ? 'You are offline. Nothing was linked.'
          : failure instanceof Error
            ? failure.message
            : 'The link could not be added.';
    } finally {
      pending = false;
    }
  }

  function onCancel(): void {
    open = false;
  }
</script>

<Dialog bind:open title="Link {title || 'this page'}" sheet={phone} {pending}>
  <form class="klink-form" id="klink-form" onsubmit={handleSubmit}>
    <Combobox
      label="Link to"
      help="Search knowledge and tasks."
      placeholder="Type a name"
      required
      search={findTargets}
      selected={target}
      onchange={onPick}
      error={missingTarget ? 'Pick a page or task to link to.' : undefined}
    />
    <SelectField label="How they relate" options={RELATIONS} bind:value={relation} />
    {#if error}
      <Notice tone="error" variant="inline">{error}</Notice>
    {/if}
  </form>
  {#snippet actions()}
    <Button variant="quiet" data-cancel disabled={pending} onclick={onCancel}>Cancel</Button>
    <Button type="submit" form="klink-form" variant="primary" {pending}>Add link</Button>
  {/snippet}
</Dialog>
