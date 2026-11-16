<script lang="ts">
  import Checkbox from '../../shared/ui/Checkbox.svelte';
  import RadioGroup from '../../shared/ui/RadioGroup.svelte';
  import Section from '../../shared/ui/Section.svelte';
  import Segmented from '../../shared/ui/Segmented.svelte';
  import SelectField from '../../shared/ui/SelectField.svelte';
  import Switch from '../../shared/ui/Switch.svelte';
  import TextArea from '../../shared/ui/TextArea.svelte';
  import TextField from '../../shared/ui/TextField.svelte';
  import { deliveryOptions, effortOptions } from '../fixtures';

  let email = $state('');
  let name = $state('Hillfield allotment');
  let note = $state('');
  let effort = $state('high');
  let delivery = $state('digest');
  let digest = $state(true);
  let quiet = $state(false);
  let syncing = $state(false);
  let view = $state('week');
  let agree = $state(false);
  let status = $state<'saving' | 'saved'>();

  function validateEmail(value: string): string | undefined {
    if (value.trim() === '') return 'Enter an email address';
    return /^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(value) ? undefined : 'Enter an address like name@example.test';
  }

  function onQuiet(next: boolean): void {
    syncing = true;
    window.setTimeout(() => {
      syncing = false;
      quiet = next;
    }, 800);
  }

  function onSaveName(): void {
    status = 'saving';
    window.setTimeout(() => (status = 'saved'), 600);
  }
</script>

<Section id="g-fields" data-gallery="fields" title="Fields">
  <div class="grid">
    <TextField
      label="Reply address"
      type="email"
      autocomplete="email"
      required
      validate={validateEmail}
      bind:value={email}
      placeholder="name@example.test"
    />
    <TextField label="Project name" {status} bind:value={name} onchange={onSaveName} />
    <SelectField label="Effort" options={effortOptions} bind:value={effort} help="Higher effort costs more" />
    <TextField label="API host" value="Set by host" disabled />
  </div>
  <TextArea label="Note" bind:value={note} rows={4} />
  <TextField label="Postcode" value="HF1" error="This postcode is outside the delivery area" />
</Section>

<Section id="g-choices" data-gallery="choices" title="Choices">
  <div class="grid">
    <div class="stack">
      <Checkbox label="Remember this device" bind:checked={agree} />
      <Checkbox label="Include archived mail" help="Older than 90 days" />
      <Checkbox label="Locked" disabled checked />
    </div>
    <RadioGroup legend="Delivery" options={deliveryOptions} bind:value={delivery} />
    <div class="stack">
      <Switch label="Morning digest" bind:checked={digest} />
      <Switch label="Quiet hours" checked={quiet} pending={syncing} onchange={onQuiet} />
      <Switch label="Remote tools" checked locked="Set by host" />
      <Segmented
        label="Range"
        options={[
    { value: 'day', label: 'Day' },
    { value: 'week', label: 'Week' },
    { value: 'month', label: 'Month' },
  ]}
        bind:value={view}
      />
    </div>
  </div>
</Section>
