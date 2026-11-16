<script lang="ts">
  import type { Setting, SettingsSchemaField } from '../../contract';
  import Badge from '../../shared/ui/Badge.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import Checkbox from '../../shared/ui/Checkbox.svelte';
  import Icon from '../../shared/ui/Icon.svelte';
  import Segmented from '../../shared/ui/Segmented.svelte';
  import SelectField from '../../shared/ui/SelectField.svelte';
  import Switch from '../../shared/ui/Switch.svelte';
  import TextField from '../../shared/ui/TextField.svelte';
  import {
    checkValue,
    durationText,
    FORMATS,
    labelOf,
    pinText,
    rangeText,
    shownValue,
    splitRange,
    unitOf,
    WEEKDAYS,
  } from './settings';

  interface Props {
    field: SettingsSchemaField;
    setting: Setting | undefined;
    value?: string;
    error?: string;
    elsewhere?: string;
    locked?: string;
    view?: boolean;
    resetting?: boolean;
    onreset: (field: SettingsSchemaField) => void;
  }

  let {
    field,
    setting,
    value = $bindable(''),
    error,
    elsewhere,
    locked,
    view = false,
    resetting = false,
    onreset,
  }: Props = $props();

  const uid = $props.id();
  const label = $derived(labelOf(field));
  const format = $derived(FORMATS[field.key]);
  const pin = $derived(setting?.source === 'pin' ? (setting.pin_source ?? '') : undefined);
  const readonly = $derived(view || pin !== undefined || !field.editable);
  const stored = $derived(setting?.value ?? '');
  const fallback = $derived(field.default ?? '');
  const differs = $derived(!field.secret && setting?.source === 'db' && stored !== fallback);
  const choices = $derived(format === 'weekdays' ? WEEKDAYS : field.choices);
  const picked = $derived(
    new Set(
      value
        .split(',')
        .map((part) => part.trim())
        .filter(Boolean),
    ),
  );
  const range = $derived(splitRange(value));
  const echo = $derived.by(() => {
    if (unitOf(field) === 'seconds' && /^\d+$/.test(value.trim())) return durationText(Number(value.trim()));
    if (format === 'time-range') return rangeText(value);
    return '';
  });

  function validate(text: string): string | undefined {
    return checkValue(field, text);
  }

  function onSwitch(checked: boolean): void {
    value = checked ? 'true' : 'false';
  }

  function onPick(choice: string, event: Event & { currentTarget: HTMLInputElement }): void {
    const next = new Set(picked);
    if (event.currentTarget.checked) next.add(choice);
    else next.delete(choice);
    value = choices
      .map((item) => item.value)
      .filter((item) => next.has(item))
      .join(',');
  }

  function onStart(event: Event & { currentTarget: HTMLInputElement }): void {
    value = `${event.currentTarget.value}-${range[1]}`;
  }

  function onEnd(event: Event & { currentTarget: HTMLInputElement }): void {
    value = `${range[0]}-${event.currentTarget.value}`;
  }

  function onReset(): void {
    onreset(field);
  }
</script>

<div class="setting" id="setting-{field.key}" data-key={field.key} data-source={setting?.source ?? 'default'} tabindex="-1">
  {#if readonly}
    <div class="setting__readonly">
      <span class="setting__label">{label}</span>
      <span class="setting__value">{shownValue(field, stored)}</span>
      {#if pin !== undefined}
        <span class="meta setting__pin"><Icon name="server" />{pinText(pin)}</span>
      {:else if view}
        <span class="meta setting__pin">{setting?.source === 'db' ? 'Set in atrium' : 'Default'}</span>
      {/if}
    </div>
  {:else if field.type === 'bool'}
    <Switch {label} help={field.help ?? undefined} checked={value === 'true'} {locked} onchange={onSwitch} />
  {:else if field.type === 'enum' && field.choices.length <= 3}
    <fieldset class="setting__group" disabled={locked !== undefined}>
      <legend class="setting__label">{label}</legend>
      <Segmented {label} options={field.choices} bind:value />
    </fieldset>
  {:else if field.type === 'enum'}
    <SelectField {label} help={field.help ?? undefined} options={field.choices} bind:value {error} disabled={locked !== undefined} />
  {:else if field.multi || format === 'weekdays'}
    <fieldset class="setting__group" disabled={locked !== undefined} aria-describedby={error ? `${uid}-error` : undefined}>
      <legend class="setting__label">{label}</legend>
      <div class="setting__choices">
        {#each choices as choice (choice.value)}
          <Checkbox
            label={choice.label}
            checked={picked.has(choice.value)}
            onchange={(event) => onPick(choice.value, event)}
          />
        {/each}
      </div>
    </fieldset>
  {:else if format === 'time-range'}
    <fieldset class="setting__group" disabled={locked !== undefined} aria-describedby={echo ? `${uid}-echo` : undefined}>
      <legend class="setting__label">{label}</legend>
      <div class="setting__range">
        <TextField label="Starts" type="time" value={range[0]} oninput={onStart} />
        <TextField label="Ends" type="time" value={range[1]} oninput={onEnd} />
      </div>
    </fieldset>
  {:else if field.secret}
    <TextField
      {label}
      type="password"
      autocomplete="new-password"
      placeholder="Stored value hidden"
      bind:value
      {error}
      readonly={locked !== undefined}
    />
  {:else}
    <TextField
      {label}
      help={field.help ?? undefined}
      bind:value
      {validate}
      {error}
      inputmode={field.type === 'int' ? 'numeric' : field.type === 'float' ? 'decimal' : undefined}
      autocomplete="off"
      spellcheck={field.type === 'string' ? undefined : false}
      readonly={locked !== undefined}
    />
  {/if}
  {#if error && (field.multi || format !== undefined || (field.type === 'enum' && field.choices.length <= 3) || field.type === 'bool')}
    <p class="field__error" id="{uid}-error"><Icon name="alert" />{error}</p>
  {/if}
  {#if echo}
    <p class="meta setting__echo" id="{uid}-echo">{echo}</p>
  {/if}
  {#if (field.help && (readonly || field.multi || format !== undefined || (field.type === 'enum' && field.choices.length <= 3)))}
    <p class="field__help">{field.help}</p>
  {/if}
  {#if field.restart || differs || elsewhere !== undefined}
    <div class="setting__facts">
      {#if field.restart && !readonly}
        <Badge tone="info">Restart atrium to apply</Badge>
      {/if}
      {#if elsewhere !== undefined}
        <Badge tone="warn">Changed elsewhere</Badge>
        <span class="meta">Now {shownValue(field, elsewhere)}</span>
      {/if}
      {#if differs}
        <span class="meta">Default {shownValue(field, fallback)}</span>
        {#if !readonly}
          <Button variant="quiet" pending={resetting} disabled={locked !== undefined} reason={locked} onclick={onReset}>
            Reset to default
          </Button>
        {/if}
      {/if}
    </div>
  {/if}
</div>
