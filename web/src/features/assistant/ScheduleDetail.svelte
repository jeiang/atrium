<script lang="ts">
  import { useQueryClient } from '@tanstack/svelte-query';
  import {
    ApiError,
    assistantScheduleCancel,
    assistantScheduleEnabled,
    assistantScheduleRunNow,
    assistantScheduleUpdate,
    ok,
    type ScheduleJob,
  } from '../../contract';
  import { refusedOffline, unreachable } from '../../shared/device.svelte';
  import Badge from '../../shared/ui/Badge.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import Dialog from '../../shared/ui/Dialog.svelte';
  import List from '../../shared/ui/List.svelte';
  import ListRow from '../../shared/ui/ListRow.svelte';
  import Notice from '../../shared/ui/Notice.svelte';
  import Section from '../../shared/ui/Section.svelte';
  import Skeleton from '../../shared/ui/Skeleton.svelte';
  import TextArea from '../../shared/ui/TextArea.svelte';
  import TextField from '../../shared/ui/TextField.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';
  import { navigate, p } from '../../shell/router';
  import { failure, fieldErrors, gesture, money, type Outcome, refresh, runStatus, useScheduleRuns } from './assistant';
  import { GRAMMAR, ownerWords } from './schedules';

  let { job, onreload }: { job: ScheduleJob; onreload: () => void } = $props();

  const client = useQueryClient();
  const runs = useScheduleRuns(() => job.id);
  const runsOffline = $derived(unreachable(runs));
  const builtin = $derived(job.source === 'builtin');

  type Draft = { name: string; schedule: string; prompt: string };
  let draft = $state<Draft>({ name: '', schedule: '', prompt: '' });
  let origin: Draft = { name: '', schedule: '', prompt: '' };
  let synced = $state<string>();
  let busy = $state<'save' | 'run' | 'enable' | 'delete'>();
  let errors = $state<Record<string, string>>({});
  let saved = $state<Outcome>();
  let ran = $state<Outcome>();
  let toggled = $state<Outcome>();
  let confirming = $state(false);
  let removeFailed = $state<string>();

  const base = $derived({ name: job.name, schedule: job.schedule, prompt: job.prompt });
  const dirty = $derived(
    draft.name.trim() !== base.name || draft.schedule.trim() !== base.schedule || draft.prompt.trim() !== base.prompt,
  );
  const last = $derived(runStatus(job.last_status));

  $effect(() => {
    const key = `${job.id}:${job.revision}`;
    if (key === synced) return;
    const next = { ...base };
    if (synced?.startsWith(`${job.id}:`)) {
      for (const field of ['name', 'schedule', 'prompt'] as const) {
        if (draft[field].trim() !== origin[field]) next[field] = draft[field];
      }
    }
    draft = next;
    origin = { ...base };
    synced = key;
  });

  function changes() {
    const out: { name?: string; schedule?: string; prompt?: string } = {};
    if (draft.name.trim() !== base.name) out.name = draft.name.trim();
    if (draft.schedule.trim() !== base.schedule) out.schedule = draft.schedule.trim();
    if (!builtin && draft.prompt.trim() !== base.prompt) out.prompt = draft.prompt.trim();
    return out;
  }

  async function onSave(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    if (!dirty || busy) return;
    busy = 'save';
    saved = undefined;
    errors = {};
    try {
      ok(await assistantScheduleUpdate(job.id, changes(), gesture(job.revision)));
      saved = { tone: 'ok', title: 'Schedule saved', text: 'The next run follows the new settings.' };
      refresh(client, 'jobs');
    } catch (error) {
      errors = fieldErrors(error);
      saved = failure(error, 'the change', onreload);
    } finally {
      busy = undefined;
    }
  }

  async function onRun(): Promise<void> {
    if (busy) return;
    busy = 'run';
    ran = undefined;
    try {
      const started = ok(await assistantScheduleRunNow(job.id, gesture()));
      ran = {
        tone: 'ok',
        title: `Started run ${started.run}`,
        text: 'It shows in the history below. Its cost counts against the scheduled share of the budget.',
      };
      refresh(client, 'jobs');
    } catch (error) {
      if (refusedOffline(error) && error instanceof Error) {
        ran = { tone: 'warn', title: 'Cannot reach atrium', text: error.message };
      } else if (error instanceof ApiError && error.status === 409 && error.code !== 'refused') {
        ran = { tone: 'warn', title: 'Already running', text: `${error.message}. Nothing new was started.` };
        refresh(client, 'jobs');
      } else if (error instanceof ApiError) {
        ran = { tone: 'error', title: 'atrium refused to run it', text: `${error.message}. Nothing was started.` };
      } else {
        ran = {
          tone: 'error',
          title: 'The outcome is unknown',
          text: 'The connection failed before atrium answered. Check the history before you try again.',
        };
        refresh(client, 'jobs');
      }
    } finally {
      busy = undefined;
    }
  }

  async function onToggle(): Promise<void> {
    if (busy) return;
    const enabled = !job.enabled;
    busy = 'enable';
    toggled = undefined;
    try {
      ok(await assistantScheduleEnabled(job.id, { enabled }, gesture()));
      toggled = {
        tone: 'ok',
        title: enabled ? 'Schedule on' : 'Schedule off',
        text: enabled ? 'It runs at its next time.' : 'It does not run until you turn it on again.',
      };
      refresh(client, 'jobs');
    } catch (error) {
      toggled = failure(error, 'the change', onreload);
    } finally {
      busy = undefined;
    }
  }

  async function onDelete(): Promise<void> {
    if (busy) return;
    busy = 'delete';
    removeFailed = undefined;
    try {
      ok(await assistantScheduleCancel(job.id, gesture()));
      confirming = false;
      refresh(client, 'jobs');
      navigate('/assistant/routines');
    } catch (error) {
      removeFailed =
        refusedOffline(error) && error instanceof Error
          ? error.message
          : error instanceof ApiError
            ? `${error.message} Nothing was deleted.`
            : 'The outcome is unknown. The connection failed before atrium answered, so it may or may not have been deleted. atrium did not try again.';
    } finally {
      busy = undefined;
    }
  }
</script>

<Section id="schedule" title={job.name}>
  <div class="asst-badges">
    <Badge tone={job.enabled ? 'ok' : 'neutral'}>{job.enabled ? 'On' : 'Off'}</Badge>
    {#if builtin}
      <Badge tone="info">Built in</Badge>
    {/if}
    {#if job.protected}
      <Badge tone="accent">Protected</Badge>
    {/if}
  </div>
  <dl class="asst-dl">
    <dt>When</dt>
    <dd data-field="cadence">{job.cadence || job.schedule}</dd>
    <dt>Next run</dt>
    <dd data-field="next">{job.enabled ? (job.next_due_text ?? 'Not planned') : 'Off'}</dd>
    <dt>Last run</dt>
    <dd data-field="last">
      {#if last}
        {last.label}{job.last_started_text ? `, ${job.last_started_text}` : ''}
        {#if job.last_error}
          <span class="asst-error">{job.last_error}</span>
        {/if}
      {:else}
        Never
      {/if}
    </dd>
    <dt>Set up by</dt>
    <dd>{builtin ? 'atrium' : (ownerWords[job.created_by] ?? job.created_by)}</dd>
  </dl>
  <div class="cluster">
    <Button variant="primary" pending={busy === 'run'} disabled={busy !== undefined && busy !== 'run'} onclick={onRun}>
      Run now
    </Button>
    <Button
      variant="secondary"
      pending={busy === 'enable'}
      disabled={(job.protected && job.enabled) || (busy !== undefined && busy !== 'enable')}
      reason={job.protected && job.enabled ? 'This schedule is protected and cannot be turned off.' : 'Wait for the current action'}
      onclick={onToggle}
    >
      {job.enabled ? 'Turn off' : 'Turn on'}
    </Button>
  </div>
  {#if job.protected && job.enabled}
    <p class="asst-meta">This schedule is protected: atrium relies on it, so it cannot be turned off or deleted.</p>
  {/if}
  {#if ran}
    <Notice tone={ran.tone} variant="inline" title={ran.title}>{ran.text}</Notice>
  {/if}
  {#if toggled}
    <Notice tone={toggled.tone} variant="inline" title={toggled.title}>{toggled.text}</Notice>
  {/if}
</Section>

<Section id="schedule-edit" title="Change it">
  <form class="stack asst-form" novalidate onsubmit={onSave} data-settings="schedule">
    <TextField label="Name" bind:value={draft.name} autocomplete="off" error={errors.name} />
    <TextField label="When" bind:value={draft.schedule} autocomplete="off" help={GRAMMAR} error={errors.schedule} />
    {#if builtin}
      <TextArea
        label="What it does"
        value={draft.prompt}
        readonly
        rows={4}
        help="Built-in schedules keep their instructions."
      />
    {:else}
      <TextArea label="What it does" bind:value={draft.prompt} rows={4} error={errors.prompt} />
    {/if}
    <div class="cluster">
      <Button type="submit" variant="primary" pending={busy === 'save'} disabled={!dirty} reason="Nothing changed">
        Save schedule
      </Button>
      {#if !builtin && !job.protected}
        <Button variant="danger" disabled={busy !== undefined} onclick={() => (confirming = true)}
          >Delete schedule</Button
        >
      {/if}
    </div>
    {#if saved}
      <Notice tone={saved.tone} variant="inline" title={saved.title}>{saved.text}</Notice>
    {/if}
  </form>
</Section>

<Section id="schedule-history" title="History">
  {#if runsOffline}
    <ViewState kind="offline" title="Cannot reach atrium" level={3}
      >The history loads when atrium answers again.</ViewState
    >
  {:else if runs.isError && !runs.data}
    <ViewState kind="error" title="The history did not load" level={3}>
      {runs.error.message}
      {#snippet action()}
        <Button variant="secondary" onclick={() => void runs.refetch()}>Retry</Button>
      {/snippet}
    </ViewState>
  {:else if !runs.data}
    <Skeleton lines={3} label="Loading the history" />
  {:else if runs.data.length === 0}
    <ViewState kind="empty" title="No runs yet" level={3}>Runs show here once it has run.</ViewState>
  {:else}
    <List label="Run history">
      {#each runs.data as run (run.id)}
        <ListRow
          title="Run {run.id}"
          meta="{run.started_text ?? ''}{run.cost > 0 ? `, ${money(run.cost)}` : ''}{run.error ? `, ${run.error}` : ''}"
          href={run.thread_id ? p('/chat/:id', { params: { id: String(run.thread_id) } }) : undefined}
        >
          {#snippet end()}
            <Badge tone={runStatus(run.status)?.tone ?? 'neutral'}>{runStatus(run.status)?.label ?? run.status}</Badge>
          {/snippet}
        </ListRow>
      {/each}
    </List>
  {/if}
</Section>

<Dialog bind:open={confirming} title="Delete {job.name}?" pending={busy === 'delete'} initialFocus="cancel">
  <p>This deletes the schedule and stops its future runs. Past runs stay in their threads. It cannot be undone here.</p>
  {#if removeFailed}
    <Notice tone="error" variant="inline">{removeFailed}</Notice>
  {/if}
  {#snippet actions()}
    <Button data-cancel disabled={busy === 'delete'} onclick={() => (confirming = false)}>Cancel</Button>
    <Button variant="danger" pending={busy === 'delete'} onclick={onDelete}>Delete schedule</Button>
  {/snippet}
</Dialog>
