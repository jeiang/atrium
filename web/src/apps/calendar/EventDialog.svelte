<script lang="ts">
  import { useQueryClient } from '@tanstack/svelte-query';
  import { untrack } from 'svelte';
  import { ApiError, type CalendarEvent } from '../../contract';
  import { device } from '../../shared/device.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import Dialog from '../../shared/ui/Dialog.svelte';
  import Notice from '../../shared/ui/Notice.svelte';
  import Skeleton from '../../shared/ui/Skeleton.svelte';
  import { toast } from '../../shared/ui/toast.svelte';
  import { p } from '../../shell/router';
  import {
    blankDraft,
    type CalEvent,
    calendarName,
    type Draft,
    dayLabel,
    draftOf,
    draftReady,
    handleCreate,
    handleDelete,
    handleUndo,
    handleUpdate,
    lock,
    locks,
    type Problem,
    problemOf,
    refreshCalendar,
    useEvent,
    whenText,
    writeTarget,
  } from './calendar.svelte';
  import EventForm from './EventForm.svelte';

  interface Props {
    open?: boolean;
    target?: CalEvent;
    day: string;
    phone: boolean;
  }

  let { open = $bindable(false), target, day, phone }: Props = $props();

  const uid = $props.id();
  const client = useQueryClient();
  let mode = $state<'view' | 'edit' | 'confirm' | 'create'>(untrack(() => (target ? 'view' : 'create')));
  let draft = $state<Draft>(untrack(() => blankDraft(day)));
  let checked = $state(false);
  let pending = $state(false);
  let problem = $state<Problem>();

  const writable = $derived(target !== undefined && writeTarget(target.calendar) !== undefined);
  const detail = useEvent(() => (open && target && writable ? target : undefined));
  const event = $derived(detail.data);
  const locked = $derived(target !== undefined && locks.keys.includes(target.uid));
  const blocked = $derived(
    !device.online ? 'Cannot reach atrium.' : locked ? 'Locked until the outcome is known.' : undefined,
  );
  const title = $derived.by(() => {
    if (mode === 'create') return 'New event';
    if (mode === 'edit') return 'Edit event';
    if (mode === 'confirm') return 'Delete this event?';
    return target?.title ?? 'Event';
  });

  function done(message: string, effects: readonly number[] | undefined, undoable: boolean | undefined, label: string) {
    open = false;
    refreshCalendar(client);
    const ids = effects ?? [];
    toast(message, {
      action: undoable && ids.length > 0 ? { label: 'Undo', run: () => void handleUndoToast(ids, label) } : undefined,
    });
  }

  async function handleUndoToast(ids: readonly number[], label: string): Promise<void> {
    try {
      await handleUndo(ids);
      toast(`Undid ${label}.`);
    } catch (error) {
      toast(error instanceof ApiError ? error.message : 'Cannot reach atrium. Nothing was undone.', { tone: 'error' });
    } finally {
      refreshCalendar(client);
    }
  }

  async function handleFailure(error: unknown, nothing: string): Promise<void> {
    problem = problemOf(error, nothing);
    if (problem.inbox && target) lock(target.uid);
    if (error instanceof ApiError && error.status === 412) {
      const fresh = await detail.refetch();
      if (fresh.data && mode === 'edit') {
        draft = draftOf(fresh.data);
        checked = false;
      }
    }
    if (error instanceof ApiError && error.status === 404) open = false;
    refreshCalendar(client);
  }

  async function handleSave(): Promise<void> {
    checked = true;
    if (pending || !draftReady(draft)) return;
    pending = true;
    problem = undefined;
    try {
      if (mode === 'create') {
        const made = await handleCreate(draft);
        done(`Added ${made.data.title}.`, made.effect_ids, made.undoable, `adding ${made.data.title}`);
      } else if (event) {
        const saved = await handleUpdate(event, draft);
        done(`Saved ${saved.data.title}.`, saved.effect_ids, saved.undoable, `the change to ${saved.data.title}`);
      }
    } catch (error) {
      await handleFailure(error, mode === 'create' ? 'Nothing was added.' : 'Nothing was changed.');
    } finally {
      pending = false;
    }
  }

  async function handleConfirmDelete(): Promise<void> {
    if (pending || !event) return;
    pending = true;
    problem = undefined;
    try {
      const gone = await handleDelete(event);
      done(`Deleted ${event.title}.`, gone.effect_ids, gone.undoable, `deleting ${event.title}`);
    } catch (error) {
      await handleFailure(error, 'Nothing was deleted.');
    } finally {
      pending = false;
    }
  }

  function startEdit(current: CalendarEvent): void {
    draft = draftOf(current);
    checked = false;
    problem = undefined;
    mode = 'edit';
  }

  function back(): void {
    problem = undefined;
    mode = 'view';
  }
</script>

<Dialog bind:open {title} sheet={phone} {pending}>
  {#if problem}
    <Notice tone={problem.tone} variant="inline" title={problem.title}>
      {problem.text}
      {#if problem.inbox}
        <a class="link" href={p('/inbox')}>Open the inbox</a>
      {/if}
    </Notice>
  {:else if locked && mode === 'view'}
    <Notice tone="error" variant="inline" title="The outcome is unknown">
      The last change to this event was not confirmed. It stays locked until the outcome is resolved.
      <a class="link" href={p('/inbox')}>Open the inbox</a>
    </Notice>
  {/if}
  {#if mode === 'create' || mode === 'edit'}
    <EventForm id="{uid}-form" bind:draft creating={mode === 'create'} {checked} onsubmit={handleSave} />
  {:else if mode === 'confirm' && event}
    <p>{event.title}, {dayLabel(event.start)}, from {calendarName(target?.calendar ?? event.calendar)}.</p>
  {:else if target}
    <dl class="cal-facts">
      <div>
        <dt>When</dt>
        <dd>{dayLabel(target.start)}, {whenText(target, target.allDay)}</dd>
      </div>
      <div>
        <dt>Calendar</dt>
        <dd>{calendarName(target.calendar)}{writable ? '' : ', read-only'}</dd>
      </div>
      {#if target.location}
        <div>
          <dt>Where</dt>
          <dd>{target.location}</dd>
        </div>
      {/if}
      {#if event?.notes}
        <div>
          <dt>Notes</dt>
          <dd class="cal-facts__notes">{event.notes}</dd>
        </div>
      {/if}
      {#if event?.recurring}
        <div>
          <dt>Repeats</dt>
          <dd>Yes</dd>
        </div>
      {/if}
    </dl>
    {#if writable && detail.isPending}
      <Skeleton lines={1} label="Loading the event" />
    {:else if writable && detail.isError && !event}
      <Notice tone="error" variant="inline" title="The event did not load">{detail.error.message}</Notice>
    {/if}
  {/if}
  {#snippet actions()}
    {#if mode === 'create' || mode === 'edit'}
      <Button
        variant="primary"
        type="submit"
        form="{uid}-form"
        {pending}
        disabled={blocked !== undefined}
        reason={blocked}
      >
        {mode === 'create' ? 'Add event' : 'Save'}
      </Button>
      {#if mode === 'edit'}
        <Button variant="secondary" disabled={pending} onclick={back}>Cancel</Button>
      {:else}
        <Button variant="secondary" data-cancel onclick={() => (open = false)}>Cancel</Button>
      {/if}
    {:else if mode === 'confirm'}
      <Button
        variant="danger"
        {pending}
        disabled={blocked !== undefined}
        reason={blocked}
        onclick={handleConfirmDelete}
      >
        Delete
      </Button>
      <Button variant="secondary" data-cancel disabled={pending} onclick={back}>Keep it</Button>
    {:else}
      {#if writable && event}
        <Button variant="secondary" disabled={blocked !== undefined} reason={blocked} onclick={() => startEdit(event)}>
          Edit
        </Button>
        <Button variant="danger" disabled={blocked !== undefined} onclick={() => (mode = 'confirm')}>Delete</Button>
      {/if}
      <Button variant="quiet" data-cancel onclick={() => (open = false)}>Close</Button>
    {/if}
  {/snippet}
</Dialog>
