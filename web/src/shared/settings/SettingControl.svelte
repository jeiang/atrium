<script lang="ts">
  import type { Setting, SettingsSchemaField } from '../../contract';
  import Badge from '../ui/Badge.svelte';
  import Button from '../ui/Button.svelte';
  import Checkbox from '../ui/Checkbox.svelte';
  import Icon from '../ui/Icon.svelte';
  import Segmented from '../ui/Segmented.svelte';
  import SelectField from '../ui/SelectField.svelte';
  import Switch from '../ui/Switch.svelte';
  import TextField from '../ui/TextField.svelte';
  import { checkValue, pinText, shownValue } from './settings';

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
  const pin = $derived(setting?.source === 'pin' ? (setting.pin_source ?? '') : undefined);
  const readonly = $derived(view || pin !== undefined || !field.editable);
  const stored = $derived(setting?.value ?? '');
  const fallback = $derived(field.default ?? '');
  const differs = $derived(!field.secret && setting?.source === 'db' && stored !== fallback);
  const segmented = $derived(field.type === 'enum' && !field.multi && field.choices.length <= 3);
  const grouped = $derived(segmented || (field.multi && field.choices.length > 0));
  const picked = $derived(
    new Set(
      value
        .split(',')
        .map((part) => part.trim())
        .filter(Boolean),
    ),
  );

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
    value = field.choices
      .map((item) => item.value)
      .filter((item) => next.has(item))
      .join(',');
  }

  function onReset(): void {
    onreset(field);
  }
</script>

<div
  class="setting"
  id="setting-{field.key}"
  data-key={field.key}
  data-source={setting?.source ?? 'default'}
  tabindex="-1"
>
  {#if readonly}
    <div class="setting__readonly">
      <span class="setting__label">{field.label}</span>
      <span class="setting__value">{field.secret ? 'Hidden' : shownValue(field, stored)}</span>
      {#if pin !== undefined}
        <span class="meta setting__pin"><Icon name="server" />{pinText(pin)}</span>
      {:else if view}
        <span class="meta setting__pin">{setting?.source === 'db' ? 'Set in atrium' : 'Default'}</span>
      {/if}
    </div>
  {:else if field.type === 'bool'}
    <Switch
      label={field.label}
      help={field.help ?? undefined}
      checked={value === 'true'}
      {locked}
      onchange={onSwitch}
    />
  {:else if segmented}
    <fieldset class="setting__group" disabled={locked !== undefined}>
      <legend class="setting__label">{field.label}</legend>
      <Segmented label={field.label} options={field.choices} bind:value />
    </fieldset>
  {:else if field.type === 'enum' && !field.multi}
    <SelectField
      label={field.label}
      help={field.help ?? undefined}
      options={field.choices}
      bind:value
      {error}
      disabled={locked !== undefined}
    />
  {:else if field.multi && field.choices.length > 0}
    <fieldset class="setting__group" disabled={locked !== undefined}>
      <legend class="setting__label">{field.label}</legend>
      <div class="setting__choices">
        {#each field.choices as choice (choice.value)}
          <Checkbox
            label={choice.label}
            checked={picked.has(choice.value)}
            onchange={(event) => onPick(choice.value, event)}
          />
        {/each}
      </div>
    </fieldset>
  {:else}
    <TextField
      label={field.label}
      help={field.help ?? undefined}
      type={field.secret ? 'password' : 'text'}
      bind:value
      {validate}
      {error}
      inputmode={field.type === 'int' ? 'numeric' : field.type === 'float' ? 'decimal' : undefined}
      autocomplete={field.secret ? 'new-password' : 'off'}
      spellcheck={field.type === 'string' && !field.secret ? undefined : false}
      readonly={locked !== undefined}
    />
  {/if}
  {#if error && (grouped || field.type === 'bool')}
    <p class="field__error" id="{uid}-error"><Icon name="alert" />{error}</p>
  {/if}
  {#if field.help && (readonly || grouped)}
    <p class="field__help">{field.help}</p>
  {/if}
  {#if (field.restart && !readonly) || differs || elsewhere !== undefined}
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
