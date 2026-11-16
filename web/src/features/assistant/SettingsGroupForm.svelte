<script lang="ts">
  import { useQueryClient } from '@tanstack/svelte-query';
  import type { Snippet } from 'svelte';
  import { ok, type SettingsSchemaField, settingsPatch } from '../../contract';
  import { unreachable } from '../../shared/device.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import Checkbox from '../../shared/ui/Checkbox.svelte';
  import Notice from '../../shared/ui/Notice.svelte';
  import Section from '../../shared/ui/Section.svelte';
  import SelectField from '../../shared/ui/SelectField.svelte';
  import Skeleton from '../../shared/ui/Skeleton.svelte';
  import TextField from '../../shared/ui/TextField.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';
  import { failure, fieldErrors, gesture, type Outcome, refresh, useSettingsGroup } from './assistant';

  let {
    id,
    title,
    groups,
    saveLabel,
    savedText,
    empty,
    intro,
  }: {
    id: string;
    title: string;
    groups: readonly string[];
    saveLabel: string;
    savedText: string;
    empty: string;
    intro?: Snippet;
  } = $props();

  const client = useQueryClient();
  const settings = useSettingsGroup(groups);
  const offline = $derived(unreachable(settings));

  let draft = $state<Record<string, string>>({});
  let synced = $state<string>();
  let pending = $state(false);
  let errors = $state<Record<string, string>>({});
  let result = $state<Outcome>();

  const fields = $derived(settings.data?.fields ?? []);
  const base = $derived(Object.fromEntries(fields.map((field) => [field.key, field.value ?? field.default ?? ''])));
  const locked = (field: SettingsSchemaField): boolean => !field.editable || field.pin_source !== undefined;
  const editable = $derived(fields.filter((field) => !locked(field)));
  const dirty = $derived(editable.some((field) => (draft[field.key] ?? '').trim() !== base[field.key]));
  const invalid = $derived(editable.some((field) => check(field)(draft[field.key] ?? '') !== undefined));

  $effect(() => {
    const data = settings.data;
    if (!data || data.revision === synced) return;
    if (synced === undefined || !dirty) draft = { ...base };
    else draft = { ...base, ...Object.fromEntries(editable.map((field) => [field.key, draft[field.key] ?? ''])) };
    synced = data.revision;
  });

  function check(field: SettingsSchemaField): (value: string) => string | undefined {
    if (field.type !== 'int' && field.type !== 'float') return () => undefined;
    return (value) => {
      const text = value.trim();
      if (text === '') return undefined;
      const number = Number(text);
      if (!Number.isFinite(number)) return 'Enter a number.';
      if (field.type === 'int' && !Number.isInteger(number)) return 'Enter a whole number.';
      if (field.min !== undefined && number < field.min) return `Enter ${field.min} or more.`;
      if (field.max !== undefined && number > field.max) return `Enter ${field.max} or less.`;
      return undefined;
    };
  }

  function help(field: SettingsSchemaField): string {
    const parts: string[] = [];
    if (field.pin_source) parts.push(`Set by the host in ${field.pin_source}.`);
    else if (!field.editable) parts.push('Set by the host.');
    if (field.help) parts.push(field.help);
    if (field.default !== undefined && field.default !== null && field.default !== '') {
      parts.push(`Default ${choiceLabel(field, field.default)}.`);
    }
    if (field.restart) parts.push('Applies after atrium restarts.');
    return parts.join(' ');
  }

  function choiceLabel(field: SettingsSchemaField, value: string): string {
    if (field.type === 'bool') return value === 'true' ? 'on' : 'off';
    return field.choices.find((choice) => choice.value === value)?.label ?? value;
  }

  function onToggle(key: string, event: Event): void {
    const target = event.currentTarget;
    if (target instanceof HTMLInputElement) draft[key] = target.checked ? 'true' : 'false';
  }

  async function onSave(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    const data = settings.data;
    if (!data || !dirty || pending) return;
    if (invalid) {
      result = { tone: 'error', title: 'Check the marked fields', text: 'Fix them, then save again.' };
      return;
    }
    const set: Record<string, string> = {};
    const unset: string[] = [];
    for (const field of editable) {
      const value = (draft[field.key] ?? '').trim();
      if (value === base[field.key]) continue;
      if (value === '') unset.push(field.key);
      else set[field.key] = value;
    }
    pending = true;
    result = undefined;
    errors = {};
    try {
      ok(
        await settingsPatch(
          { ...(Object.keys(set).length > 0 ? { set } : {}), ...(unset.length > 0 ? { unset } : {}) },
          gesture(data.revision),
        ),
      );
      result = { tone: 'ok', title: 'Saved', text: savedText };
      refresh(client, 'settings');
    } catch (error) {
      errors = fieldErrors(error);
      result =
        Object.keys(errors).length > 0
          ? { tone: 'error', title: 'atrium refused some values', text: 'Fix the marked fields. Nothing was saved.' }
          : failure(error, 'the change', () => void settings.refetch());
    } finally {
      pending = false;
    }
  }

  function onRetry(): void {
    void settings.refetch();
  }
</script>

<Section {id} {title}>
  {@render intro?.()}
  {#if offline}
    <ViewState kind="offline" title="Cannot reach atrium" level={3}>These settings load when atrium answers again.</ViewState>
  {:else if settings.isError && !settings.data}
    <ViewState kind="error" title="These settings did not load" level={3}>
      {settings.error.message}
      {#snippet action()}
        <Button variant="secondary" onclick={onRetry}>Retry</Button>
      {/snippet}
    </ViewState>
  {:else if !settings.data}
    <Skeleton lines={4} label="Loading {title}" />
  {:else if fields.length === 0}
    <ViewState kind="empty" title="Nothing to set here" level={3}>{empty}</ViewState>
  {:else}
    <form class="stack asst-form" novalidate onsubmit={onSave} data-settings={id}>
      {#each fields as field (field.key)}
        <div class="asst-form__field" data-key={field.key}>
          {#if locked(field)}
            <TextField
              label={field.label}
              value={choiceLabel(field, draft[field.key] ?? base[field.key] ?? '')}
              readonly
              help={help(field)}
            />
          {:else if field.type === 'bool'}
            <Checkbox
              label={field.label}
              help={errors[field.key] ?? help(field)}
              checked={draft[field.key] === 'true'}
              onchange={(event) => onToggle(field.key, event)}
            />
          {:else if field.type === 'enum'}
            <SelectField
              label={field.label}
              bind:value={draft[field.key]}
              options={field.choices}
              help={help(field)}
              error={errors[field.key]}
            />
          {:else}
            <TextField
              label={field.label}
              bind:value={draft[field.key]}
              validate={check(field)}
              inputmode={field.type === 'int' ? 'numeric' : field.type === 'float' ? 'decimal' : undefined}
              autocomplete="off"
              help={help(field)}
              error={errors[field.key]}
            />
          {/if}
        </div>
      {/each}
      <div class="cluster">
        <Button type="submit" variant="primary" {pending} disabled={!dirty} reason="Nothing changed">{saveLabel}</Button>
      </div>
      {#if result}
        <Notice tone={result.tone} variant="inline" title={result.title}>{result.text}</Notice>
      {/if}
    </form>
  {/if}
</Section>
