<script lang="ts">
  import { useQueryClient } from '@tanstack/svelte-query';
  import { tick } from 'svelte';
  import type { Task, TaskUpdateRequest } from '../../contract';
  import { formatDayTime } from '../../shared/assistant/format';
  import Button from '../../shared/ui/Button.svelte';
  import Notice from '../../shared/ui/Notice.svelte';
  import SelectField from '../../shared/ui/SelectField.svelte';
  import StatusMark from '../../shared/ui/StatusMark.svelte';
  import TextArea from '../../shared/ui/TextArea.svelte';
  import TextField from '../../shared/ui/TextField.svelte';
  import { navigate } from '../../shell/router';
  import {
    doneToast,
    dueText,
    fromLocalInput,
    handleTaskUpdate,
    handleTaskVerb,
    linkHref,
    linkKinds,
    linkText,
    type Problem,
    priorities,
    problemOf,
    refreshPlan,
    type Status,
    statusWords,
    toLocalInput,
    type Verb,
    verbWords,
  } from './plan';

  let { task }: { task: Task } = $props();

  const client = useQueryClient();
  let editing = $state(false);
  let busy = $state(false);
  let problem = $state<Problem>();
  let title = $state('');
  let notes = $state('');
  let due = $state('');
  let remind = $state('');
  let priority = $state('normal');
  let linkKind = $state('');
  let linkId = $state('');
  let titleField = $state<HTMLElement>();

  const link = $derived(task.links[0]);
  const verbs = $derived<Verb[]>(task.status === 'open' ? ['done', 'dismiss'] : ['reopen']);

  async function onEdit(): Promise<void> {
    problem = undefined;
    title = task.title;
    notes = task.notes;
    due = task.due?.slice(0, 10) ?? '';
    remind = toLocalInput(task.remind_at);
    priority = task.priority || 'normal';
    linkKind = link?.kind ?? '';
    linkId = link?.id ?? '';
    editing = true;
    await tick();
    titleField?.querySelector('input')?.focus();
  }

  function onCancel(): void {
    editing = false;
    problem = undefined;
  }

  async function onSave(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    if (busy || !title.trim()) return;
    const clear: string[] = [];
    const body: TaskUpdateRequest = { title: title.trim(), priority, updated_ts: task.updated_ts };
    if (notes.trim()) body.notes = notes.trim();
    else if (task.notes) clear.push('notes');
    if (due) body.due = due;
    else if (task.due) clear.push('due');
    if (remind) body.remind_at = fromLocalInput(remind);
    else if (task.remind_at) clear.push('remind_at');
    if (linkKind && linkId.trim()) {
      if (linkKind !== link?.kind || linkId.trim() !== link?.id)
        body.link = { kind: linkKind as NonNullable<TaskUpdateRequest['link']>['kind'], id: linkId.trim() };
    } else if (link) clear.push('link');
    if (clear.length > 0) body.clear = clear;
    problem = undefined;
    busy = true;
    try {
      const effects = await handleTaskUpdate(task.id, body);
      editing = false;
      doneToast(`Saved: ${body.title}`, effects, client);
    } catch (error) {
      problem = problemOf(error);
      if (problem.tone === 'warn') editing = false;
    } finally {
      busy = false;
      refreshPlan(client);
    }
  }

  async function onVerb(verb: Verb | 'delete'): Promise<void> {
    if (busy) return;
    problem = undefined;
    busy = true;
    try {
      const effects = await handleTaskVerb(task.id, verb);
      doneToast(`${verbWords[verb].done}: ${task.title}`, effects, client);
      if (verb === 'delete') await navigate('/plan/tasks');
    } catch (error) {
      problem = problemOf(error);
    } finally {
      busy = false;
      refreshPlan(client);
    }
  }
</script>

<article class="card plan-detail" aria-labelledby="task-detail-title">
  {#if problem}
    <Notice tone={problem.tone} variant="inline" title={problem.title}>{problem.text}</Notice>
  {/if}
  {#if editing}
    <h2 class="plan-detail__title" id="task-detail-title">Edit task</h2>
    <form class="plan-form" onsubmit={onSave}>
      <div bind:this={titleField}>
        <TextField label="Title" bind:value={title} required maxlength={500} />
      </div>
      <div class="plan-form__row">
        <TextField label="Due" type="date" bind:value={due} />
        <SelectField label="Priority" options={priorities} bind:value={priority} />
      </div>
      <TextField label="Remind me" type="datetime-local" bind:value={remind} />
      <TextArea label="Notes" bind:value={notes} rows={4} />
      <div class="plan-form__row">
        <SelectField label="Link to" options={linkKinds} bind:value={linkKind} />
        {#if linkKind}
          <TextField
            label={linkKind === 'person' ? 'Email address' : 'Number'}
            bind:value={linkId}
            required
            inputmode={linkKind === 'person' ? 'email' : 'numeric'}
          />
        {/if}
      </div>
      <div class="plan-detail__actions">
        <Button variant="primary" type="submit" pending={busy}>Save task</Button>
        <Button variant="quiet" onclick={onCancel}>Cancel</Button>
      </div>
    </form>
  {:else}
    <p class="plan-detail__eyebrow">
      Task
      {#if task.status !== 'open'}
        <StatusMark tone="ok">{statusWords[task.status as Status]}</StatusMark>
      {/if}
    </p>
    <h2 class="plan-detail__title" id="task-detail-title">{task.title}</h2>
    {#if task.notes}
      <p class="plan-detail__notes">{task.notes}</p>
    {/if}
    <dl class="plan-detail__facts">
      {#if task.due}
        <dt>Due</dt>
        <dd>{dueText(task.due)}</dd>
      {/if}
      {#if task.remind_at}
        <dt>Reminder</dt>
        <dd>{formatDayTime(task.remind_at * 1000)}</dd>
      {/if}
      {#if task.priority && task.priority !== 'normal'}
        <dt>Priority</dt>
        <dd>{task.priority}</dd>
      {/if}
      <dt>Added</dt>
      <dd>{formatDayTime(task.created_ts * 1000)}</dd>
      {#each task.links as item (`${item.kind}:${item.id}`)}
        <dt>Link</dt>
        <dd>
          {#if linkHref(item)}
            <a class="link" href={linkHref(item)}>{linkText(item)}</a>
          {:else}
            {linkText(item)}
          {/if}
        </dd>
      {/each}
    </dl>
    <div class="plan-detail__actions">
      {#each verbs as verb (verb)}
        <Button variant={verb === 'dismiss' ? 'secondary' : 'primary'} pending={busy} onclick={() => onVerb(verb)}>
          {verbWords[verb].label}
        </Button>
      {/each}
      <Button variant="secondary" onclick={onEdit}>Edit</Button>
      <Button variant="danger" pending={busy} onclick={() => onVerb('delete')}>Delete</Button>
    </div>
  {/if}
</article>
