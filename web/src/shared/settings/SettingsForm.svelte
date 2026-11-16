<script lang="ts">
  import { useQueryClient } from '@tanstack/svelte-query';
  import { blockNavigation } from 'sv-router';
  import { type Snippet, tick, untrack } from 'svelte';
  import { ApiError, ok, type SettingsSchemaField, settingsPatch } from '../../contract';
  import { announce } from '../announce.svelte';
  import { formatClock } from '../assistant/format';
  import { refusedOffline, unreachable } from '../device.svelte';
  import Button from '../ui/Button.svelte';
  import Dialog from '../ui/Dialog.svelte';
  import Notice from '../ui/Notice.svelte';
  import Skeleton from '../ui/Skeleton.svelte';
  import ViewState from '../ui/ViewState.svelte';
  import SettingControl from './SettingControl.svelte';
  import { checkValue, refreshSettings, useSettingsSchema, useSettingsValues } from './settings';
  import './settings.css';

  interface Props {
    groups: readonly string[];
    title: string;
    id?: string;
    focus?: string;
    view?: boolean;
    locked?: string;
    phone?: boolean;
    head?: Snippet;
    intro?: Snippet;
  }

  let { groups, title, id, focus, view = false, locked, phone = false, head, intro }: Props = $props();

  type Result = { tone: 'ok' | 'warn' | 'error'; title: string; text: string };

  const client = useQueryClient();
  const schema = useSettingsSchema();
  const values = useSettingsValues();
  const offline = $derived(unreachable(schema) || unreachable(values));
  const failed = $derived((schema.isError && !schema.data) || (values.isError && !values.data));
  const scope = $derived(groups.join(' '));
  const fields = $derived(
    groups.flatMap((group) =>
      (schema.data?.pages ?? []).flatMap((page) => page.fields.filter((field) => field.group === group)),
    ),
  );
  const items = $derived(values.data?.items ?? {});
  const writable = $derived(view ? [] : fields.filter((field) => field.editable && items[field.key]?.source !== 'pin'));
  const base = $derived(
    Object.fromEntries(fields.map((field) => [field.key, field.secret ? '' : (items[field.key]?.value ?? '')])),
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

  const changed = $derived(
    writable.filter((field) => (draft[field.key] ?? '').trim() !== (origin[field.key] ?? '').trim()),
  );
  const dirty = $derived(changed.length > 0);

  $effect(() => {
    const revision = values.data?.revision;
    const key = scope;
    const ready = schema.data !== undefined;
    untrack(() => {
      if (revision === undefined || !ready) return;
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
    if (!key || shown !== scope) return;
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

  function onRetry(): void {
    if (schema.isError) void schema.refetch();
    if (values.isError) void values.refetch();
  }

  function names(list: readonly SettingsSchemaField[]): string {
    const labels = list.map((field) => field.label);
    if (labels.length <= 1) return labels.join('');
    return `${labels.slice(0, -1).join(', ')} and ${labels.at(-1)}`;
  }

  function fieldErrors(error: ApiError): Record<string, string> {
    const body = error.body;
    const env = typeof body === 'object' && body !== null && 'error' in body ? body.error : undefined;
    const list =
      typeof env === 'object' && env !== null && 'fields' in env && Array.isArray(env.fields) ? env.fields : [];
    const out: Record<string, string> = {};
    for (const item of list) {
      if (typeof item !== 'object' || item === null) continue;
      const key = 'loc' in item && Array.isArray(item.loc) ? item.loc[1] : undefined;
      const msg = 'msg' in item ? item.msg : undefined;
      if (typeof key !== 'string' || typeof msg !== 'string') continue;
      out[key] = msg.startsWith(`${key}: `) ? msg.slice(key.length + 2) : msg;
    }
    return out;
  }

  function failure(error: unknown, what: string): Result {
    if (refusedOffline(error) && error instanceof Error) {
      return { tone: 'warn', title: 'Cannot reach atrium', text: error.message };
    }
    if (error instanceof ApiError && error.status === 412) {
      void values.refetch();
      return {
        tone: 'warn',
        title: 'The settings changed elsewhere',
        text: 'atrium reloaded them and kept your edits. Settings changed elsewhere are marked; save again to replace them.',
      };
    }
    if (error instanceof ApiError && error.status === 403) {
      return {
        tone: 'error',
        title: 'This sign-in cannot change settings',
        text: `${error.message} Nothing was saved.`,
      };
    }
    if (error instanceof ApiError) {
      const found = fieldErrors(error);
      if (Object.keys(found).length > 0) {
        errors = found;
        return { tone: 'error', title: `atrium refused ${what}`, text: 'Fix the marked settings. Nothing was saved.' };
      }
      return { tone: 'error', title: `atrium refused ${what}`, text: `${error.message} Nothing was saved.` };
    }
    void values.refetch();
    return {
      tone: 'error',
      title: 'The outcome is unknown',
      text: 'The connection failed before atrium answered. atrium reloaded the settings and did not try again.',
    };
  }

  async function onSave(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    const revision = values.data?.revision;
    if (!dirty || pending || locked || revision === undefined) return;
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
      const headers = { 'Idempotency-Key': crypto.randomUUID(), 'If-Match': `W/"${revision}"` };
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
    const revision = values.data?.revision;
    if (pending || resetting || locked || revision === undefined) return;
    resetting = field.key;
    draft[field.key] = origin[field.key] ?? '';
    result = undefined;
    try {
      const headers = { 'Idempotency-Key': crypto.randomUUID(), 'If-Match': `W/"${revision}"` };
      ok(await settingsPatch({ unset: [field.key] }, { headers }));
      result = {
        tone: 'ok',
        title: `Reset at ${formatClock(new Date())}`,
        text: `${field.label} is back to its default.${field.restart ? ' Restart atrium to apply it.' : ''}`,
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

<form class="settings-form" {id} novalidate onsubmit={onSave} aria-labelledby="{id ?? 'settings-form'}-title">
  <div class="settings-form__head">
    {@render head?.()}
    <h2 class="settings-form__title" id="{id ?? 'settings-form'}-title">{title}</h2>
  </div>
  {@render intro?.()}
  {#if offline}
    <ViewState kind="offline" title="Cannot reach atrium">
      These settings load when atrium answers again. Nothing was changed.
    </ViewState>
  {:else if failed}
    <ViewState kind="error" title="These settings did not load">
      {schema.error?.message ?? values.error?.message}
      {#snippet action()}
        <Button variant="secondary" onclick={onRetry}>Retry</Button>
      {/snippet}
    </ViewState>
  {:else if !schema.data || !values.data || shown !== scope}
    <Skeleton lines={4} label="Loading the settings" />
  {:else if fields.length === 0}
    <ViewState kind="empty" title="No settings here">atrium offers no settings for this page.</ViewState>
  {:else}
    {#each fields as field (field.key)}
      <SettingControl
        {field}
        setting={items[field.key]}
        bind:value={() => draft[field.key] ?? '', (next) => (draft[field.key] = next)}
        error={errors[field.key]}
        elsewhere={marks[field.key]}
        {locked}
        {view}
        resetting={resetting === field.key}
        onreset={onReset}
      />
    {/each}
  {/if}
  <div class="settings-form__result" tabindex="-1" bind:this={notice}>
    {#if result}
      <Notice tone={result.tone} variant="inline" title={result.title}>{result.text}</Notice>
    {/if}
  </div>
  {#if writable.length > 0 && (dirty || pending)}
    <div class="settings-form__bar">
      <span class="meta">Unsaved changes</span>
      <Button type="submit" variant="primary" {pending} disabled={locked !== undefined} reason={locked}>Save</Button>
      <Button variant="quiet" disabled={pending} onclick={onDiscard}>Discard</Button>
    </div>
  {/if}
</form>

<Dialog bind:open={leaving} title="Leave without saving?" initialFocus="cancel" sheet={phone} onclose={onLeaveClosed}>
  <p>Your changes to {title} are not saved. Leaving discards them.</p>
  {#snippet actions()}
    <Button variant="quiet" data-cancel onclick={onKeep}>Keep editing</Button>
    <Button variant="danger" onclick={onLeave}>Discard changes</Button>
  {/snippet}
</Dialog>
