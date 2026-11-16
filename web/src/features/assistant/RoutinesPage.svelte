<script lang="ts">
  import { useQueryClient } from '@tanstack/svelte-query';
  import { assistantScheduleAdd, ok } from '../../contract';
  import { unreachable } from '../../shared/device.svelte';
  import Badge from '../../shared/ui/Badge.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import Dialog from '../../shared/ui/Dialog.svelte';
  import List from '../../shared/ui/List.svelte';
  import ListRow from '../../shared/ui/ListRow.svelte';
  import Notice from '../../shared/ui/Notice.svelte';
  import Page from '../../shared/ui/Page.svelte';
  import Section from '../../shared/ui/Section.svelte';
  import Skeleton from '../../shared/ui/Skeleton.svelte';
  import TextArea from '../../shared/ui/TextArea.svelte';
  import TextField from '../../shared/ui/TextField.svelte';
  import { toast } from '../../shared/ui/toast.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';
  import { p, route } from '../../shell/router';
  import AssistantNav from './AssistantNav.svelte';
  import { failure, fieldErrors, gesture, type Outcome, refresh, runStatus, useSchedules } from './assistant';
  import ScheduleDetail from './ScheduleDetail.svelte';
  import SettingsGroupForm from './SettingsGroupForm.svelte';
  import { GRAMMAR } from './schedules';
  import './assistant.css';

  const client = useQueryClient();
  const schedules = useSchedules();
  const offline = $derived(unreachable(schedules));
  const selected = $derived(route.params.id ? Number(route.params.id) : undefined);
  const job = $derived(schedules.data?.items.find((item) => item.id === selected));

  let creating = $state(false);
  let confirmStep = $state(false);
  let pending = $state(false);
  let draft = $state({ name: '', schedule: '', prompt: '' });
  let errors = $state<Record<string, string>>({});
  let result = $state<Outcome>();

  function onOpen(): void {
    draft = { name: '', schedule: '', prompt: '' };
    errors = {};
    result = undefined;
    confirmStep = false;
    creating = true;
  }

  function onReview(event: SubmitEvent): void {
    event.preventDefault();
    const missing: Record<string, string> = {};
    if (draft.name.trim() === '') missing.name = 'Give it a name.';
    if (draft.schedule.trim() === '') missing.schedule = 'Say when it runs.';
    if (draft.prompt.trim() === '') missing.prompt = 'Say what it does.';
    errors = missing;
    if (Object.keys(missing).length === 0) confirmStep = true;
  }

  async function onCreate(): Promise<void> {
    if (pending) return;
    pending = true;
    result = undefined;
    try {
      const made = ok(
        await assistantScheduleAdd(
          { name: draft.name.trim(), schedule: draft.schedule.trim(), prompt: draft.prompt.trim() },
          gesture(),
        ),
      );
      creating = false;
      toast(`Set up ${draft.name.trim()}. Next run: ${made.job?.next_due_text ?? 'planned'}.`);
      refresh(client, 'jobs');
    } catch (error) {
      errors = fieldErrors(error);
      confirmStep = false;
      result = failure(error, 'the schedule', () => void schedules.refetch());
    } finally {
      pending = false;
    }
  }
</script>

<Page title="Routines" eyebrow="Assistant" lede="Work the assistant does on a schedule, and what it costs.">
  <AssistantNav current="routines" />
  <div class="asst">
    {#if selected !== undefined}
      <div class="cluster"><a class="asst-link" href={p('/assistant/routines')}>All schedules</a></div>
      {#if job}
        <ScheduleDetail {job} onreload={() => void schedules.refetch()} />
      {:else if offline}
        <ViewState kind="offline" title="Cannot reach atrium">The schedule loads when atrium answers again.</ViewState>
      {:else if schedules.isError && !schedules.data}
        <ViewState kind="error" title="The schedule did not load">
          {schedules.error.message}
          {#snippet action()}
            <Button variant="secondary" onclick={() => void schedules.refetch()}>Retry</Button>
          {/snippet}
        </ViewState>
      {:else if !schedules.data}
        <Skeleton lines={5} label="Loading the schedule" />
      {:else}
        <ViewState kind="empty" title="No such schedule">It may have been deleted.</ViewState>
      {/if}
    {:else}
      <Section id="schedules" title="Schedules" meta={schedules.data ? `Times in ${schedules.data.tz}` : undefined}>
        {#snippet actions()}
          <Button variant="primary" onclick={onOpen}>New schedule</Button>
        {/snippet}
        {#if offline}
          <ViewState kind="offline" title="Cannot reach atrium" level={3}
            >Schedules load when atrium answers again.</ViewState
          >
        {:else if schedules.isError && !schedules.data}
          <ViewState kind="error" title="Schedules did not load" level={3}>
            {schedules.error.message}
            {#snippet action()}
              <Button variant="secondary" onclick={() => void schedules.refetch()}>Retry</Button>
            {/snippet}
          </ViewState>
        {:else if !schedules.data}
          <Skeleton lines={4} label="Loading schedules" />
        {:else if schedules.data.items.length === 0}
          <ViewState kind="empty" title="No schedules" level={3}
            >Set one up to have the assistant check something regularly.</ViewState
          >
        {:else}
          <List label="Schedules">
            {#each schedules.data.items as item (item.id)}
              <ListRow
                title={item.name}
                meta="{item.cadence || item.schedule}. Next: {item.enabled ? (item.next_due_text ?? 'not planned') : 'off'}{item.last_status ? `. Last: ${runStatus(item.last_status)?.label ?? item.last_status}` : ''}"
                href={p('/assistant/routines/:id', { params: { id: String(item.id) } })}
              >
                {#snippet end()}
                  <Badge tone={item.enabled ? 'ok' : 'neutral'}>{item.enabled ? 'On' : 'Off'}</Badge>
                {/snippet}
              </ListRow>
            {/each}
          </List>
        {/if}
      </Section>
      <SettingsGroupForm
        id="routine-settings"
        title="Routine settings"
        groups={['assistant.routines']}
        saveLabel="Save routine settings"
        savedText="The next scheduled or nightly run uses them."
        empty="atrium offers no routine settings right now."
      />
    {/if}
  </div>
</Page>

<Dialog bind:open={creating} title={confirmStep ? `Set up ${draft.name.trim()}?` : 'New schedule'} {pending}>
  {#if confirmStep}
    <p>It runs on "{draft.schedule.trim()}". Each run is paid from the scheduled share of the budget.</p>
  {:else}
    <form id="schedule-new" class="stack asst-form" novalidate onsubmit={onReview}>
      <TextField label="Name" bind:value={draft.name} autocomplete="off" error={errors.name} />
      <TextField label="When" bind:value={draft.schedule} autocomplete="off" help={GRAMMAR} error={errors.schedule} />
      <TextArea label="What it does" bind:value={draft.prompt} rows={4} error={errors.prompt} />
    </form>
    {#if result}
      <Notice tone={result.tone} variant="inline" title={result.title}>{result.text}</Notice>
    {/if}
  {/if}
  {#snippet actions()}
    {#if confirmStep}
      <Button data-cancel disabled={pending} onclick={() => (confirmStep = false)}>Back</Button>
      <Button variant="primary" {pending} onclick={onCreate}>Set up schedule</Button>
    {:else}
      <Button data-cancel onclick={() => (creating = false)}>Cancel</Button>
      <Button type="submit" form="schedule-new" variant="primary">Review</Button>
    {/if}
  {/snippet}
</Dialog>
