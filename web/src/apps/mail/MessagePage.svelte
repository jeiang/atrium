<script lang="ts">
  import { useQueryClient } from '@tanstack/svelte-query';
  import { formatDayTime } from '../../shared/assistant/format';
  import { inboxPath } from '../../shared/assistant/inboxId';
  import type { Stuck } from '../../shared/assistant/stuck';
  import { device, unreachable } from '../../shared/device.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import Notice from '../../shared/ui/Notice.svelte';
  import Page from '../../shared/ui/Page.svelte';
  import Section from '../../shared/ui/Section.svelte';
  import Skeleton from '../../shared/ui/Skeleton.svelte';
  import { toast } from '../../shared/ui/toast.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';
  import { appGone, useApps } from '../../shell/manifest';
  import NotFound from '../../shell/NotFound.svelte';
  import { p, route } from '../../shell/router';
  import Compose, { type Start } from './Compose.svelte';
  import MailNav from './MailNav.svelte';
  import {
    type Done,
    effectOf,
    handleArchive,
    handleFlag,
    handleRead,
    handleReply,
    handleResolveEffect,
    handleUndo,
    type Problem,
    problemOf,
    refreshMail,
    sizeText,
    unknownOutcome,
    useMessage,
    useQueueLink,
  } from './mail';
  import Unresolved from './Unresolved.svelte';
  import './mail.css';

  type Action = 'archive' | 'read' | 'flag' | 'reply';

  const client = useQueryClient();
  const apps = useApps();
  const app = $derived(apps.data?.find((item) => item.id === 'mail'));
  const id = $derived.by(() => {
    const raw = route.params.id ?? '';
    return /^[1-9][0-9]{0,15}$/.test(raw) ? Number(raw) : undefined;
  });
  const message = useMessage(() => id);
  const queued = useQueueLink(() => (message.data ? id : undefined));
  const mail = $derived(message.data ?? undefined);
  const offline = $derived(!device.online);
  const offReason = $derived(offline ? 'Cannot reach atrium.' : undefined);
  const headers = $derived.by(() => {
    if (!mail) return [];
    const rows: [string, string | undefined][] = [
      ['From', mail.headers.From],
      ['To', mail.headers.To],
      ['Cc', mail.headers.Cc],
      ['Date', mail.date_ts ? formatDayTime(mail.date_ts * 1000) : mail.headers.Date],
    ];
    return rows.flatMap(([name, value]) => (value ? [{ name, value }] : []));
  });

  let busy = $state<Action>();
  let problem = $state<Problem>();
  let stuck = $state<{ kind: Stuck; effect: number }>();
  let archived = $state(false);
  let composing = $state(false);
  let start = $state<Start>({ kind: 'new' });

  function saidOf(action: Exclude<Action, 'reply'>, on: boolean): { done: string; undone: string } {
    if (action === 'archive') return { done: 'Archived.', undone: 'Moved back to the inbox.' };
    if (action === 'read')
      return on
        ? { done: 'Marked as read.', undone: 'Marked as unread again.' }
        : { done: 'Marked as unread.', undone: 'Marked as read again.' };
    return on
      ? { done: 'Flagged.', undone: 'Flag removed again.' }
      : { done: 'Flag removed.', undone: 'Flagged again.' };
  }

  async function act(action: Exclude<Action, 'reply'>, on: boolean, run: () => Promise<Done>): Promise<void> {
    if (busy || stuck) return;
    busy = action;
    problem = undefined;
    const said = saidOf(action, on);
    try {
      const done = await run();
      if (action === 'archive') archived = true;
      const effect = done.effect;
      toast(
        said.done,
        effect === undefined ? {} : { action: { label: 'Undo', run: () => void onUndo(effect, action, said.undone) } },
      );
    } catch (error) {
      const effect = effectOf(error);
      if (unknownOutcome(error) && effect !== undefined) stuck = { kind: 'forward', effect };
      else problem = problemOf(error);
    } finally {
      busy = undefined;
      refreshMail(client);
    }
  }

  async function onUndo(effect: number, action: Exclude<Action, 'reply'>, undone: string): Promise<void> {
    if (!device.online) {
      toast('Cannot reach atrium. Nothing was undone.', { tone: 'error' });
      return;
    }
    try {
      await handleUndo(effect);
      if (action === 'archive') archived = false;
      toast(undone);
    } catch (error) {
      const blocked = effectOf(error);
      if (unknownOutcome(error)) stuck = { kind: 'undo', effect: blocked ?? effect };
      else toast(problemOf(error).title, { tone: 'error' });
    } finally {
      refreshMail(client);
    }
  }

  async function onReply(): Promise<void> {
    if (!mail || busy) return;
    busy = 'reply';
    problem = undefined;
    try {
      start = { kind: 'draft', draft: await handleReply(mail.id) };
      composing = true;
    } catch (error) {
      problem = problemOf(error);
    } finally {
      busy = undefined;
    }
  }

  function onForward(): void {
    if (!mail) return;
    start = { kind: 'forward', message: mail.id, subject: mail.subject };
    composing = true;
  }

  async function onResolve(outcome: string): Promise<void> {
    if (!stuck) return;
    await handleResolveEffect(stuck.effect, outcome);
    refreshMail(client);
  }

  function onResolved(said: string): void {
    stuck = undefined;
    toast(said);
  }
</script>

{#if appGone(apps, 'mail')}
  <NotFound />
{:else}
  <Page title={mail?.subject || (message.data === null ? 'Message not found' : 'Message')} eyebrow="Mail">
    <a class="link mail-back" href={p('/apps/mail')}>All messages</a>
    <MailNav {app} />
    {#if id === undefined || message.data === null}
      <ViewState kind="empty" title="This message is not here">
        It may have been deleted, or the link is wrong.
        {#snippet action()}
          <a class="link" href={p('/apps/mail')}>Back to messages</a>
        {/snippet}
      </ViewState>
    {:else if unreachable(message) || (offline && !message.data)}
      <ViewState kind="offline" title="Cannot reach atrium">
        The message loads when atrium answers again. Nothing from the mailbox is kept on this device.
      </ViewState>
    {:else if message.isError && !message.data}
      <ViewState kind="error" title="The message did not load">
        {message.error.message}
        {#snippet action()}
          <Button variant="secondary" onclick={() => void message.refetch()}>Retry</Button>
        {/snippet}
      </ViewState>
    {:else if !mail}
      <Skeleton lines={6} label="Loading the message" />
    {:else}
      <article class="mail-message" aria-label="Message">
        <dl class="mail-head">
          {#each headers as { name, value } (name)}
            <div class="mail-head__row">
              <dt>{name}</dt>
              <dd>{value}</dd>
            </div>
          {/each}
        </dl>
        <div class="mail-actions">
          <Button
            variant="primary"
            icon="reply"
            pending={busy === 'reply'}
            disabled={offline || stuck !== undefined}
            reason={offReason}
            onclick={() => void onReply()}
            >Reply</Button
          >
          <Button
            variant="secondary"
            icon="forward"
            disabled={offline || stuck !== undefined}
            reason={offReason}
            onclick={onForward}
            >Forward</Button
          >
          {#if mail.in_inbox && !archived}
            <Button
              variant="secondary"
              icon="archive"
              pending={busy === 'archive'}
              disabled={offline || stuck !== undefined}
              reason={offReason}
              onclick={() => void act('archive', true, () => handleArchive(mail.id))}
              >Archive</Button
            >
          {/if}
          <Button
            variant="secondary"
            pending={busy === 'read'}
            disabled={offline || stuck !== undefined}
            reason={offReason}
            onclick={() => void act('read', !mail.seen, () => handleRead(mail.id, !mail.seen))}
            >{mail.seen ? 'Mark as unread' : 'Mark as read'}</Button
          >
          <Button
            variant="secondary"
            icon="flag"
            pending={busy === 'flag'}
            disabled={offline || stuck !== undefined}
            reason={offReason}
            onclick={() => void act('flag', !mail.flagged, () => handleFlag(mail.id, !mail.flagged))}
            >{mail.flagged ? 'Remove flag' : 'Flag'}</Button
          >
          {#if queued.data}
            <a class="btn btn--quiet" href={inboxPath('loop', queued.data.replace(/^loop:/, ''))}>Open in Inbox</a>
          {/if}
        </div>
        {#if problem}
          <Notice tone={problem.tone} title={problem.title}>{problem.text}</Notice>
        {/if}
        {#if stuck}
          <Unresolved
            stuck={stuck.kind}
            title={stuck.kind === 'undo' ? 'The undo outcome is unknown' : 'The outcome is unknown'}
            subject={mail.subject || 'This message'}
            onresolve={onResolve}
            ondone={onResolved}
          />
        {/if}
        {#if mail.body_source === 'index'}
          <Notice tone="info" variant="inline" title="Showing the saved copy">
            The mail server did not answer, so this is the text atrium saved when it read the message.
          </Notice>
        {/if}
        <pre class="mail-body">{mail.body || 'This message has no text.'}</pre>
        {#if mail.attachments.length > 0}
          <Section title="Attachments" meta={String(mail.attachments.length)}>
            <ul class="mail-files">
              {#each mail.attachments as file, index (index)}
                <li>
                  <span>{file.filename || 'Unnamed file'}</span>
                  <span class="mail-files__meta">{file.content_type}, {sizeText(file.size)}</span>
                </li>
              {/each}
            </ul>
            <p class="meta">Attachments open in your mail app. atrium lists them but does not download them.</p>
          </Section>
        {/if}
      </article>
    {/if}
  </Page>
  <Compose bind:open={composing} {start} accounts={[]} onchanged={() => refreshMail(client)} />
{/if}
