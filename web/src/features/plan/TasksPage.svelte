<script lang="ts">
  import { useQueryClient } from '@tanstack/svelte-query';
  import { on } from 'svelte/events';
  import type { Task, TaskAddRequest } from '../../contract';
  import { plural } from '../../shared/assistant/format';
  import Button from '../../shared/ui/Button.svelte';
  import List from '../../shared/ui/List.svelte';
  import ListRow from '../../shared/ui/ListRow.svelte';
  import Notice from '../../shared/ui/Notice.svelte';
  import Page from '../../shared/ui/Page.svelte';
  import Section from '../../shared/ui/Section.svelte';
  import Segmented from '../../shared/ui/Segmented.svelte';
  import SelectField from '../../shared/ui/SelectField.svelte';
  import Skeleton from '../../shared/ui/Skeleton.svelte';
  import TextField from '../../shared/ui/TextField.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';
  import { p, route } from '../../shell/router';
  import PlanNav from './PlanNav.svelte';
  import {
    bucketOf,
    bucketWords,
    doneToast,
    dueText,
    grouped,
    handleTaskAdd,
    handleTaskVerb,
    isModuleOff,
    linkKinds,
    linkWords,
    type Problem,
    priorities,
    problemOf,
    refreshPlan,
    type Status,
    statuses,
    statusWords,
    useTask,
    useTasks,
    type Verb,
    verbWords,
  } from './plan';
  import TaskDetail from './TaskDetail.svelte';
  import './plan.css';

  const client = useQueryClient();
  let phone = $state(false);
  let status = $state<Status>('open');
  let typed = $state('');
  let q = $state('');
  let busy = $state<number>();
  let adding = $state(false);
  let problem = $state<Problem>();
  let newTitle = $state('');
  let newDue = $state('');
  let newPriority = $state('normal');
  let newLinkKind = $state('');
  let newLinkId = $state('');
  let addField = $state<HTMLElement>();

  const id = $derived.by(() => {
    const raw = Number(route.params.id);
    return Number.isSafeInteger(raw) && raw > 0 ? raw : undefined;
  });
  const badAddress = $derived(route.params.id !== undefined && id === undefined);
  const detail = useTask(() => id);
  const list = useTasks(() => ({ status, q }));
  const items = $derived(list.data?.pages.flatMap((page) => page.items) ?? []);
  const counts = $derived(list.data?.pages[0]?.counts);
  const off = $derived(isModuleOff(list.error));
  const offline = $derived(list.isPending && list.fetchStatus === 'paused');
  const overdue = $derived(status === 'open' ? items.filter((item) => bucketOf(item.due) === 'overdue').length : 0);
  const lede = $derived.by(() => {
    if (!counts) return undefined;
    if (counts.open === 0) return 'Nothing to do.';
    const owed = `${plural(counts.open, 'task')} open`;
    return overdue > 0 ? `${owed}, ${overdue} overdue` : owed;
  });
  const statusOptions = $derived(
    statuses.map((value) => ({
      value,
      label: counts ? `${statusWords[value]} ${counts[value]}` : statusWords[value],
    })),
  );
  const groups = $derived(status === 'open' ? grouped(items) : [{ bucket: undefined, items }]);

  $effect(() => {
    const value = typed.trim();
    const handle = setTimeout(() => (q = value), 250);
    return () => clearTimeout(handle);
  });

  $effect(() => {
    const query = matchMedia('(width < 861px)');
    phone = query.matches;
    return on(query, 'change', () => (phone = query.matches));
  });

  function meta(task: Task): string {
    return [
      dueText(task.due),
      task.priority && task.priority !== 'normal' ? `${task.priority} priority` : undefined,
      ...task.links.map((link) => linkWords[link.kind] ?? link.kind),
    ]
      .filter(Boolean)
      .join(' · ');
  }

  async function onAdd(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    const title = newTitle.trim();
    if (!title || adding) return;
    const body: TaskAddRequest = { title };
    if (newDue) body.due = newDue;
    if (newPriority !== 'normal') body.priority = newPriority;
    if (newLinkKind && newLinkId.trim())
      body.link = { kind: newLinkKind as NonNullable<TaskAddRequest['link']>['kind'], id: newLinkId.trim() };
    problem = undefined;
    adding = true;
    try {
      const added = await handleTaskAdd(body);
      doneToast(`Added: ${title}`, added.effects, client);
      newTitle = '';
      newDue = '';
      newPriority = 'normal';
      newLinkKind = '';
      newLinkId = '';
      addField?.querySelector('input')?.focus();
    } catch (error) {
      problem = problemOf(error);
    } finally {
      adding = false;
      refreshPlan(client);
    }
  }

  async function onVerb(task: Task, verb: Verb): Promise<void> {
    if (busy) return;
    problem = undefined;
    busy = task.id;
    try {
      const effects = await handleTaskVerb(task.id, verb);
      doneToast(`${verbWords[verb].done}: ${task.title}`, effects, client);
    } catch (error) {
      problem = problemOf(error);
    } finally {
      busy = undefined;
      refreshPlan(client);
    }
  }

  function onMore(): void {
    void list.fetchNextPage();
  }

  function onRetry(): void {
    void list.refetch();
  }
</script>

{#snippet rowEnd(
    task: Task,
  )}
  {#if task.status === 'open'}
    <Button
      size="sm"
      variant="quiet"
      icon="check"
      pending={busy === task.id}
      label="Done: {task.title}"
      onclick={() => onVerb(task, 'done')}
    >
      Done
    </Button>
  {:else}
    <Button
      size="sm"
      variant="quiet"
      icon="undo"
      pending={busy === task.id}
      label="Reopen: {task.title}"
      onclick={() => onVerb(task, 'reopen')}
    >
      Reopen
    </Button>
  {/if}
{/snippet}

{#snippet listBody()}
  {#if off}
    <ViewState kind="empty" title="Tasks are off">Turn on the Assistant app to keep tasks.</ViewState>
  {:else if offline}
    <ViewState kind="offline" title="You are offline">Your tasks load when the connection is back.</ViewState>
  {:else if list.isError && !list.data}
    <ViewState kind="error" title="Your tasks did not load">
      {list.error.message}
      {#snippet action()}
        <Button variant="secondary" onclick={onRetry}>Retry</Button>
      {/snippet}
    </ViewState>
  {:else if list.isPending}
    <div class="plan__loading"><Skeleton lines={8} label="Loading your tasks" /></div>
  {:else if items.length === 0}
    <ViewState kind="empty" title={q ? 'Nothing matches' : `No ${statusWords[status].toLowerCase()} tasks`} />
  {:else}
    {#each groups as group (group.bucket ?? 'all')}
      <Section title={group.bucket ? bucketWords[group.bucket] : statusWords[status]} meta={String(group.items.length)}>
        <List>
          {#each group.items as task (task.id)}
            <ListRow
              title={task.title}
              meta={meta(task)}
              href={p('/plan/tasks/:id', { params: { id: String(task.id) } })}
              selected={task.id === id}
              pending={busy === task.id}
            >
              {#snippet end()}
                {@render rowEnd(task)}
              {/snippet}
            </ListRow>
          {/each}
        </List>
      </Section>
    {/each}
    {#if list.hasNextPage}
      <Button variant="secondary" pending={list.isFetchingNextPage} onclick={onMore}>Load more</Button>
    {/if}
  {/if}
{/snippet}

{#snippet detailBody()}
  {#if badAddress}
    <ViewState kind="empty" title="Nothing here">This address does not name a task.</ViewState>
  {:else if detail.data}
    {#key detail.data.id}
      <TaskDetail task={detail.data} />
    {/key}
  {:else if detail.isPending}
    <div class="card plan__loading"><Skeleton lines={8} label="Loading the task" /></div>
  {:else if detail.isError}
    <ViewState kind="error" title="This task did not load">{detail.error.message}</ViewState>
  {:else}
    <ViewState kind="empty" title="Nothing here">
      This task was deleted, or the address is wrong.
      {#snippet action()}
        <a class="btn btn--secondary" href={p('/plan/tasks')}>All tasks</a>
      {/snippet}
    </ViewState>
  {/if}
{/snippet}

<Page title="Tasks" {lede}>
  <PlanNav current="tasks" />
  {#if problem}
    <Notice tone={problem.tone} variant="inline" title={problem.title}>{problem.text}</Notice>
  {/if}
  {#if phone && route.params.id !== undefined}
    <div class="plan__pane">
      <a class="link plan__back" href={p('/plan/tasks')}>All tasks</a>
      {@render detailBody()}
    </div>
  {:else}
    <div class="plan">
      <div class="plan__list">
        {#if !off}
          <form class="plan-add" aria-label="Add a task" onsubmit={onAdd}>
            <div class="plan-add__main" bind:this={addField}>
              <TextField label="New task" bind:value={newTitle} required maxlength={500} autocomplete="off" />
              <Button variant="primary" type="submit" icon="plus" pending={adding}>Add task</Button>
            </div>
            <details class="plan-add__more">
              <summary>Due date, priority and link</summary>
              <div class="plan-form__row">
                <TextField label="Due" type="date" bind:value={newDue} />
                <SelectField label="Priority" options={priorities} bind:value={newPriority} />
              </div>
              <div class="plan-form__row">
                <SelectField label="Link to" options={linkKinds} bind:value={newLinkKind} />
                {#if newLinkKind}
                  <TextField
                    label={newLinkKind === 'person' ? 'Email address' : 'Number'}
                    bind:value={newLinkId}
                    required
                    inputmode={newLinkKind === 'person' ? 'email' : 'numeric'}
                  />
                {/if}
              </div>
            </details>
          </form>
          <div class="plan__filters">
            <Segmented label="Status" options={statusOptions} bind:value={status} />
            <TextField label="Search" type="search" bind:value={typed} autocomplete="off" />
          </div>
        {/if}
        {@render listBody()}
      </div>
      {#if !phone}
        <div class="plan__pane">
          {#if route.params.id !== undefined}
            {@render detailBody()}
          {:else if !off}
            <ViewState kind="empty" title="Pick a task">Its details, links and edits show here.</ViewState>
          {/if}
        </div>
      {/if}
    </div>
  {/if}
</Page>
