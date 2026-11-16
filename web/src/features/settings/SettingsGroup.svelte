<script lang="ts">
  import { useQueryClient } from '@tanstack/svelte-query';
  import { blockNavigation } from 'sv-router';
  import { tick, untrack } from 'svelte';
  import { ApiError, ok, type SettingsSchemaField, settingsPatch } from '../../contract';
  import { announce } from '../../shared/announce.svelte';
  import { formatClock } from '../../shared/assistant/format';
  import { refusedOffline } from '../../shared/device.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import Dialog from '../../shared/ui/Dialog.svelte';
  import Notice from '../../shared/ui/Notice.svelte';
  import { useShell } from '../../shell/frame';
  import { p } from '../../shell/router';
  import type { SettingsGroup } from './placement';
  import SettingControl from './SettingControl.svelte';
  import { checkValue, labelOf, refreshSettings, type SettingsValues } from './settings';

  interface Props {
    group: SettingsGroup;
    fields: readonly SettingsSchemaField[];
    values: SettingsValues;
    focus?: string;
    locked?: string;
    phone: boolean;
    onreload: () => void;
  }

  let { group, fields, values, focus, locked, phone, onreload }: Props = $props();

  type Result = { tone: 'ok' | 'warn' | 'error'; title: string; text: string };

  const client = useQueryClient();
  const shell = useShell();
  const view = $derived(group.id === 'apps');
  const writable = $derived(fields.filter((field) => !view && field.editable && values.items[field.key]?.source !== 'pin'));
  const base = $derived(
    Object.fromEntries(fields.map((field) => [field.key, field.secret ? '' : (values.items[field.key]?.value ?? '')])),
  );

  let draft = $state<Record<string, string>>({});
  let origin = $state<Record<string, string>>({});
  let marks = $state<Record<string, string>>({});
  let errors = $state<Record<string, string>>({});
  let synced = $state<string>();
  let shown = $state<string>();
  let pending = $state(false);
  let resetting = $state<string>();
  let result = $state<Result>();
  let leaving = $state(false);
  let settle: ((leave: boolean) => void) | undefined;
  let notice = $state<HTMLElement>();

  const changed = $derived(writable.filter((field) => (draft[field.key] ?? '').trim() !== (origin[field.key] ?? '').trim()));
  const dirty = $derived(changed.length > 0);

  $effect(() => {
    const revision = values.revision;
    const key = group.id;
    untrack(() => {
      if (shown !== key) {
        draft = { ...base };
        origin = { ...base };
        marks = {};
        errors = {};
        result = undefined;
        shown = key;
        synced = revision;
        return;
      }
      if (revision === synced) return;
      const nextDraft = { ...draft };
      const nextMarks = { ...marks };
      for (const field of fields) {
        const k = field.key;
        if ((draft[k] ?? '') === (origin[k] ?? '')) nextDraft[k] = base[k] ?? '';
        else if ((base[k] ?? '') !== (origin[k] ?? '')) nextMarks[k] = base[k] ?? '';
      }
      draft = nextDraft;
      marks = nextMarks;
      origin = { ...base };
      synced = revision;
    });
  });

  $effect(() => {
    const key = focus;
    if (!key || shown !== group.id) return;
    void tick().then(() => {
      const row = document.getElementById(`setting-${key}`);
      if (!row) return;
      row.scrollIntoView({ block: 'center' });
      (row.querySelector<HTMLElement>('input, select, button, a') ?? row).focus({ preventScroll: true });
    });
  });

  $effect(() => {
    if (result) void tick().then(() => notice?.focus());
  });

  $effect(() =>
    blockNavigation({
      beforeUnload: () => !dirty,
      onNavigate: () => !dirty || ask(),
    }),
  );

  function ask(): Promise<boolean> {
    settle?.(false);
    leaving = true;
    return new Promise((resolve) => {
      settle = resolve;
    });
  }

  function answer(leave: boolean): void {
    const done = settle;
    settle = undefined;
    leaving = false;
    done?.(leave);
  }

  function onKeep(): void {
    answer(false);
  }

  function onLeave(): void {
    answer(true);
  }

  function onLeaveClosed(): void {
    answer(false);
  }

  function names(list: readonly SettingsSchemaField[]): string {
    const labels = list.map((field) => labelOf(field));
    if (labels.length <= 1) return labels.join('');
    return `${labels.slice(0, -1).join(', ')} and ${labels.at(-1)}`;
  }

  function fieldErrors(error: ApiError): Record<string, string> {
    const env = (error.body as { error?: { fields?: { loc?: unknown[]; msg?: string }[] } } | undefined)?.error;
    const out: Record<string, string> = {};
    for (const item of env?.fields ?? []) {
      const key = item.loc?.[1];
      if (typeof key !== 'string' || !item.msg) continue;
      out[key] = item.msg.startsWith(`${key}: `) ? item.msg.slice(key.length + 2) : item.msg;
    }
    return out;
  }

  function failure(error: unknown, what: string): Result {
    if (refusedOffline(error) && error instanceof Error) {
      return { tone: 'warn', title: 'Cannot reach atrium', text: error.message };
    }
    if (error instanceof ApiError && error.status === 412) {
      onreload();
      return {
        tone: 'warn',
        title: 'The settings changed elsewhere',
        text: 'atrium reloaded them and kept your edits. Settings changed elsewhere are marked; save again to replace them.',
      };
    }
    if (error instanceof ApiError && error.status === 403) {
      return { tone: 'error', title: 'This sign-in cannot change settings', text: `${error.message} Nothing was saved.` };
    }
    if (error instanceof ApiError) {
      const found = fieldErrors(error);
      if (Object.keys(found).length > 0) {
        errors = found;
        return { tone: 'error', title: `atrium refused ${what}`, text: 'Fix the marked settings. Nothing was saved.' };
      }
      return { tone: 'error', title: `atrium refused ${what}`, text: `${error.message} Nothing was saved.` };
    }
    onreload();
    return {
      tone: 'error',
      title: 'The outcome is unknown',
      text: 'The connection failed before atrium answered. atrium reloaded the settings and did not try again.',
    };
  }

  async function onSave(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    if (!dirty || pending || locked) return;
    const local: Record<string, string> = {};
    for (const field of changed) {
      const message = checkValue(field, draft[field.key] ?? '');
      if (message) local[field.key] = message;
    }
    errors = local;
    if (Object.keys(local).length > 0) {
      result = { tone: 'error', title: 'Check the marked settings', text: 'Fix them, then save again.' };
      return;
    }
    const set: Record<string, string> = {};
    const unset: string[] = [];
    for (const field of changed) {
      const value = (draft[field.key] ?? '').trim();
      if (value === '') unset.push(field.key);
      else set[field.key] = value;
    }
    const saving = [...changed];
    pending = true;
    result = undefined;
    try {
      const headers = { 'Idempotency-Key': crypto.randomUUID(), 'If-Match': `W/"${values.revision}"` };
      ok(
        await settingsPatch(
          { ...(Object.keys(set).length > 0 ? { set } : {}), ...(unset.length > 0 ? { unset } : {}) },
          { headers },
        ),
      );
      for (const field of saving) {
        const value = (draft[field.key] ?? '').trim();
        draft[field.key] = value;
        origin[field.key] = value;
        delete marks[field.key];
      }
      const restart = saving.filter((field) => field.restart);
      result = {
        tone: 'ok',
        title: `Saved at ${formatClock(new Date())}`,
        text: `Changed ${names(saving)}.${restart.length > 0 ? ` Restart atrium to apply ${names(restart)}; until then the old value stays in use.` : ''}`,
      };
      announce(result.title);
      refreshSettings(client);
    } catch (error) {
      result = failure(error, 'the change');
    } finally {
      pending = false;
    }
  }

  function onDiscard(): void {
    draft = { ...origin };
    marks = {};
    errors = {};
    result = undefined;
  }

  async function onReset(field: SettingsSchemaField): Promise<void> {
    if (pending || resetting || locked) return;
    resetting = field.key;
    draft[field.key] = origin[field.key] ?? '';
    result = undefined;
    try {
      const headers = { 'Idempotency-Key': crypto.randomUUID(), 'If-Match': `W/"${values.revision}"` };
      ok(await settingsPatch({ unset: [field.key] }, { headers }));
      result = {
        tone: 'ok',
        title: `Reset at ${formatClock(new Date())}`,
        text: `${labelOf(field)} is back to its default.${field.restart ? ' Restart atrium to apply it.' : ''}`,
      };
      announce(result.title);
      refreshSettings(client);
    } catch (error) {
      result = failure(error, 'the reset');
    } finally {
      resetting = undefined;
    }
  }
</script>

<form class="settings-group" novalidate onsubmit={onSave} aria-labelledby="settings-group-title">
  <div class="settings-group__head">
    {#if phone}
      <a class="link settings-group__back" href={p('/settings')}>All settings</a>
    {/if}
    <h2 class="settings-group__title" id="settings-group-title">{group.title}</h2>
  </div>
  {#if group.id === 'settings.general'}
    <div class="setting">
      <div class="setting__readonly">
        <span class="setting__label">Weather location</span>
        <a class="link" href={p('/settings/connections/:kind/:name', { params: { kind: 'service', name: 'weather' } })}>
          Set on the weather connection
        </a>
      </div>
    </div>
  {:else if group.id === 'connections'}
    <div class="setting">
      <div class="setting__readonly">
        <span class="setting__label">Accounts and keys</span>
        <a class="link" href={p('/settings/connections')}>Open the connection list</a>
      </div>
    </div>
  {:else if group.id === 'apps.mail'}
    <div class="setting" id="setting-mail_mode" data-key="mail_mode" tabindex="-1">
      <div class="setting__readonly">
        <span class="setting__label">Mail mode</span>
        <span class="setting__value">
          {#if shell.data?.mail}
            {shell.data.mail.mode === 'live' ? 'Live' : 'Practice (shadow)'}{shell.data.mail.paused ? ', paused' : ''}
          {:else}
            Not known
          {/if}
        </span>
        <span class="meta setting__pin">
          Changed on the host with <code>atrium settings set mail_mode live</code> or <code>shadow</code>
        </span>
      </div>
    </div>
  {/if}
  {#each fields as field (field.key)}
    <SettingControl
      {field}
      setting={values.items[field.key]}
      bind:value={draft[field.key]}
      error={errors[field.key]}
      elsewhere={marks[field.key]}
      {locked}
      {view}
      resetting={resetting === field.key}
      onreset={onReset}
    />
  {/each}
  <div class="settings-group__result" tabindex="-1" bind:this={notice}>
    {#if result}
      <Notice tone={result.tone} variant="inline" title={result.title}>{result.text}</Notice>
    {/if}
  </div>
  {#if writable.length > 0 && (dirty || pending)}
    <div class="settings-group__bar">
      <span class="meta">Unsaved changes</span>
      <Button type="submit" variant="primary" {pending} disabled={locked !== undefined} reason={locked}>Save</Button>
      <Button variant="quiet" disabled={pending} onclick={onDiscard}>Discard</Button>
    </div>
  {/if}
</form>

<Dialog bind:open={leaving} title="Leave without saving?" initialFocus="cancel" sheet={phone} onclose={onLeaveClosed}>
  <p>Your changes to {group.title} are not saved. Leaving discards them.</p>
  {#snippet actions()}
    <Button variant="quiet" data-cancel onclick={onKeep}>Keep editing</Button>
    <Button variant="danger" onclick={onLeave}>Discard changes</Button>
  {/snippet}
</Dialog>
