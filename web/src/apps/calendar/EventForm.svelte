<script lang="ts">
  import Checkbox from '../../shared/ui/Checkbox.svelte';
  import SelectField from '../../shared/ui/SelectField.svelte';
  import TextArea from '../../shared/ui/TextArea.svelte';
  import TextField from '../../shared/ui/TextField.svelte';
  import { type Draft, targets } from './calendar.svelte';

  interface Props {
    id: string;
    draft: Draft;
    creating?: boolean;
    checked?: boolean;
    onsubmit: () => void;
  }

  let { id, draft = $bindable(), creating = false, checked = false, onsubmit }: Props = $props();

  const missingTitle = $derived(checked && !draft.title.trim() ? 'Give the event a title.' : undefined);
  const missingStart = $derived(checked && !draft.start ? 'Say when it starts.' : undefined);
  const endError = $derived(checked ? endProblem(draft.end) : undefined);

  function endProblem(value: string): string | undefined {
    if (!value || !draft.start) return undefined;
    if (draft.allDay ? value < draft.start : value <= draft.start) return 'It has to end after it starts.';
    return undefined;
  }

  function onAllDay(event: Event & { currentTarget: HTMLInputElement }): void {
    const whole = event.currentTarget.checked;
    if (whole) {
      draft.start = draft.start.slice(0, 10);
      draft.end = draft.end ? draft.end.slice(0, 10) : '';
    } else {
      draft.start = draft.start ? `${draft.start.slice(0, 10)}T09:00` : '';
      draft.end = draft.end ? `${draft.end.slice(0, 10)}T10:00` : '';
    }
  }

  function handleSubmit(event: SubmitEvent): void {
    event.preventDefault();
    onsubmit();
  }
</script>

<form class="cal-form" {id} novalidate onsubmit={handleSubmit}>
  {#if creating}
    <SelectField label="Calendar" options={targets} bind:value={draft.calendar} />
  {/if}
  <TextField label="Title" required autocomplete="off" bind:value={draft.title} error={missingTitle} />
  <Checkbox label="All day" bind:checked={draft.allDay} onchange={onAllDay} />
  <div class="cal-form__when">
    {#if draft.allDay}
      <TextField label="Starts" type="date" required bind:value={draft.start} error={missingStart} />
      <TextField
        label="Ends"
        type="date"
        min={draft.start || undefined}
        bind:value={draft.end}
        error={endError}
        validate={endProblem}
      />
    {:else}
      <TextField label="Starts" type="datetime-local" required bind:value={draft.start} error={missingStart} />
      <TextField
        label="Ends"
        type="datetime-local"
        min={draft.start || undefined}
        bind:value={draft.end}
        error={endError}
        validate={endProblem}
      />
    {/if}
  </div>
  <TextField label="Location" autocomplete="off" bind:value={draft.location} />
  <TextArea label="Notes" rows={3} bind:value={draft.notes} />
</form>
