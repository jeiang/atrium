<script lang="ts" module>
  import type { Draft } from './mail';

  export type Start = { kind: 'new' } | { kind: 'forward'; message: number; subject: string } | { kind: 'draft'; draft: Draft };
</script>

<script lang="ts">
  const touchQuery = '(width < 861px), ((pointer: coarse) and (not (any-pointer: fine)))';

  import { untrack } from 'svelte';
  import { on } from 'svelte/events';
  import { announce } from '../../shared/announce.svelte';
  import HoldButton from '../../shared/assistant/HoldButton.svelte';
  import ReceiptLine, { UNDO_MS } from '../../shared/assistant/ReceiptLine.svelte';
  import { device } from '../../shared/device.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import Dialog from '../../shared/ui/Dialog.svelte';
  import Notice from '../../shared/ui/Notice.svelte';
  import SelectField from '../../shared/ui/SelectField.svelte';
  import TextArea from '../../shared/ui/TextArea.svelte';
  import TextField from '../../shared/ui/TextField.svelte';
  import { toast } from '../../shared/ui/toast.svelte';
  import Unresolved from './Unresolved.svelte';
  import {
    firstName,
    handleCreate,
    handleDiscard,
    handleForward,
    handleResolveDraft,
    handleSave,
    handleSend,
    type MailAccount,
    type Problem,
    problemOf,
    unknownOutcome,
  } from './mail';

  interface Props {
    open?: boolean;
    start: Start;
    accounts: readonly MailAccount[];
    onchanged: () => void;
  }

  let { open = $bindable(false), start, accounts, onchanged }: Props = $props();

  let touch = $state(false);
  let draft = $state<Draft>();
  let account = $state('');
  let to = $state('');
  let cc = $state('');
  let subject = $state('');
  let body = $state('');
  let phase = $state<'edit' | 'saving' | 'counting' | 'sending' | 'unknown'>('edit');
  let left = $state(0);
  let attempt = $state(0);
  let problem = $state<Problem>();
  let countTimer: number | undefined;

  const forwarding = $derived(start.kind === 'forward' && !draft);
  const title = $derived(
    forwarding ? 'Forward' : draft?.in_reply_to_message_id ? 'Reply' : draft ? 'Edit draft' : 'New email',
  );
  const dirty = $derived(
    draft
      ? to !== draft.to || cc !== draft.cc || subject !== draft.subject || body !== draft.body
      : to !== '' || subject !== '' || body !== '',
  );
  const complete = $derived(to.trim() !== '' && body.trim() !== '' && (draft !== undefined || account !== ''));
  const busy = $derived(phase === 'saving' || phase === 'sending');
  const locked = $derived(phase !== 'edit');
  const offline = $derived(!device.online);
  const options = $derived(accounts.filter((item) => item.enabled).map((item) => ({ value: item.id, label: item.label || item.login || item.id })));

  $effect(() => {
    const query = matchMedia(touchQuery);
    touch = query.matches;
    return on(query, 'change', () => (touch = query.matches));
  });

  $effect(() => {
    if (!account && options[0]) account = options[0].value;
  });

  $effect(() => {
    const begin = start;
    untrack(() => {
      stopCount();
      problem = undefined;
      phase = 'edit';
      attempt += 1;
      if (begin.kind === 'draft') fill(begin.draft);
      else {
        draft = undefined;
        to = '';
        cc = '';
        subject = '';
        body = '';
      }
    });
  });

  $effect(() => () => stopCount());

  function fill(next: Draft): void {
    draft = next;
    to = next.to;
    cc = next.cc;
    subject = next.subject;
    body = next.body;
  }

  function stopCount(): void {
    clearInterval(countTimer);
    countTimer = undefined;
  }

  async function save(): Promise<Draft> {
    if (!draft) {
      const made = await handleCreate({ account, to, cc: cc || undefined, subject, body });
      fill(made);
      return made;
    }
    if (!dirty) return draft;
    const saved = await handleSave(draft.id, { to, cc, subject, body, revision: draft.revision });
    fill(saved);
    return saved;
  }

  async function onContinue(): Promise<void> {
    if (start.kind !== 'forward' || phase !== 'edit') return;
    phase = 'saving';
    problem = undefined;
    try {
      fill(await handleForward(start.message, to.trim(), cc.trim()));
      onchanged();
    } catch (error) {
      problem = problemOf(error);
    } finally {
      phase = 'edit';
    }
  }

  async function onSaveDraft(): Promise<void> {
    if (phase !== 'edit') return;
    phase = 'saving';
    problem = undefined;
    try {
      await save();
      toast('Draft saved.');
      onchanged();
    } catch (error) {
      problem = problemOf(error);
    } finally {
      phase = 'edit';
    }
  }

  async function prepared(): Promise<Draft | undefined> {
    phase = 'saving';
    problem = undefined;
    try {
      return await save();
    } catch (error) {
      problem = problemOf(error);
      phase = 'edit';
      attempt += 1;
      return undefined;
    }
  }

  async function deliver(ready: Draft): Promise<void> {
    stopCount();
    phase = 'sending';
    try {
      const sent = await handleSend(ready.id, ready.revision);
      draft = sent.draft;
      open = false;
      const who = firstName(sent.draft.to);
      toast(
        sent.refused.length > 0
          ? `Sent to ${who}. The server refused ${sent.refused.join(', ')}.`
          : `Sent to ${who}.`,
        { tone: sent.refused.length > 0 ? 'error' : 'neutral' },
      );
      phase = 'edit';
    } catch (error) {
      if (unknownOutcome(error)) {
        phase = 'unknown';
        announce('The send outcome is unknown.', 'assertive');
      } else {
        problem = problemOf(error);
        phase = 'edit';
        attempt += 1;
      }
    } finally {
      onchanged();
    }
  }

  async function onSend(): Promise<void> {
    if (phase !== 'edit') return;
    const ready = await prepared();
    if (!ready) return;
    if (touch) {
      await deliver(ready);
      return;
    }
    phase = 'counting';
    left = UNDO_MS / 1000;
    announce(`Sending in ${left} seconds. Press Undo to stop.`);
    countTimer = window.setInterval(() => {
      left -= 1;
      if (left > 0) return;
      void deliver(ready);
    }, 1000);
  }

  function onStop(): void {
    if (phase !== 'counting') return;
    stopCount();
    phase = 'edit';
    attempt += 1;
    announce('Not sent. The draft is saved.');
  }

  let confirming = $state(false);

  async function onConfirm(): Promise<void> {
    confirming = false;
    if (phase !== 'edit') return;
    const ready = await prepared();
    if (ready) await deliver(ready);
  }

  async function onDiscard(): Promise<void> {
    if (phase !== 'edit') return;
    if (!draft) {
      open = false;
      return;
    }
    phase = 'saving';
    try {
      await handleDiscard(draft.id);
      draft = undefined;
      open = false;
      toast('Draft discarded.');
      onchanged();
    } catch (error) {
      problem = problemOf(error);
    } finally {
      phase = 'edit';
    }
  }

  async function onResolve(outcome: string): Promise<void> {
    if (!draft) return;
    const resolved = await handleResolveDraft(draft.id, outcome === 'sent' ? 'sent' : 'draft');
    onchanged();
    if (outcome === 'sent') {
      open = false;
      return;
    }
    fill(resolved);
    phase = 'edit';
    attempt += 1;
  }

  function onClosed(): void {
    if (phase === 'counting') onStop();
  }
</script>

<Dialog bind:open {title} sheet={touch} pending={busy} onclose={onClosed}>
  <form class="mail-compose" inert={locked || undefined} onsubmit={(event) => event.preventDefault()}>
    {#if !draft && start.kind === 'new'}
      <SelectField label="From" {options} bind:value={account} required />
    {/if}
    <TextField
      label="To"
      bind:value={to}
      required
      autocomplete="email"
      inputmode="email"
      enterkeyhint="next"
      help="Separate several addresses with commas."
    />
    <TextField label="Cc" bind:value={cc} autocomplete="email" inputmode="email" enterkeyhint="next" />
    {#if !forwarding}
      <TextField label="Subject" bind:value={subject} enterkeyhint="next" />
      <TextArea label="Message" bind:value={body} required rows={10} />
      {#if draft?.unexpected_recipients?.length}
        <Notice tone="warn" variant="inline" title="Check the recipients">
          {draft.unexpected_recipients.join(', ')}
          {draft.unexpected_recipients.length === 1 ? 'was' : 'were'} not on the original message.
        </Notice>
      {/if}
    {:else}
      <p class="mail-compose__note">atrium adds the original message below your note once you continue.</p>
    {/if}
  </form>
  {#if problem}
    <Notice tone={problem.tone} variant="inline" title={problem.title}>{problem.text}</Notice>
  {/if}
  {#if offline && phase !== 'unknown'}
    <Notice tone="warn" variant="inline" title="Cannot reach atrium">
      The draft stays here. Sending is off until atrium answers again.
    </Notice>
  {/if}
  {#if phase === 'unknown' && draft}
    <Unresolved
      stuck="send"
      title="The send outcome is unknown"
      subject="Email to {firstName(draft.to)}"
      onresolve={onResolve}
      ondone={(said) => toast(said)}
    />
  {:else if phase === 'counting'}
    <ReceiptLine tone="pending" title="Sending to {firstName(to)} in {left} s" onundo={onStop} />
  {/if}
  {#snippet actions()}
    {#if forwarding}
      <Button variant="quiet" onclick={() => (open = false)}>Cancel</Button>
      <Button
        variant="primary"
        pending={phase === 'saving'}
        disabled={offline || to.trim() === ''}
        reason={offline ? 'Cannot reach atrium.' : to.trim() === '' ? 'Add a recipient first.' : undefined}
        onclick={() => void onContinue()}
        >Continue</Button
      >
    {:else if phase !== 'unknown'}
      <Button variant="quiet" disabled={locked} onclick={() => void onDiscard()}>{draft ? 'Discard' : 'Cancel'}</Button>
      <Button
        variant="secondary"
        pending={phase === 'saving'}
        disabled={locked || offline || !complete || !dirty}
        reason={offline ? 'Cannot reach atrium.' : undefined}
        onclick={() => void onSaveDraft()}
        >Save draft</Button
      >
      {#key attempt}
        <HoldButton
          label="Send"
          presentation={touch ? 'hold' : 'click'}
          gated={false}
          disabled={locked || offline || !complete}
          onconfirm={() => (confirming = true)}
          onreveal={() => undefined}
          onsend={() => void onSend()}
        />
      {/key}
    {/if}
  {/snippet}
</Dialog>

<Dialog bind:open={confirming} title="Send this now?" initialFocus="cancel">
  <p>Email to {firstName(to)}. Once sent it cannot be recalled.</p>
  {#snippet actions()}
    <Button variant="quiet" data-cancel onclick={() => (confirming = false)}>Cancel</Button>
    <Button variant="primary" disabled={offline} onclick={() => void onConfirm()}>Send now</Button>
  {/snippet}
</Dialog>
