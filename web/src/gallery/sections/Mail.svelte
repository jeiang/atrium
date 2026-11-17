<script lang="ts">
  import Compose, { type Start } from '../../apps/mail/Compose.svelte';
  import MessageCard from '../../apps/mail/MessageCard.svelte';
  import { sizeText } from '../../apps/mail/mail';
  import Unresolved from '../../apps/mail/Unresolved.svelte';
  import '../../apps/mail/mail.css';
  import { mailAccountsSeed, seedMailbox } from '../../mocks/mail';
  import ReceiptLine from '../../shared/assistant/ReceiptLine.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import Notice from '../../shared/ui/Notice.svelte';
  import Section from '../../shared/ui/Section.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';

  const rows = seedMailbox();
  const hits = rows.map((item) => item.hit);
  const renewal = rows.find((item) => item.hit.message_id === 102);
  let open = $state(false);
  let start = $state<Start>({ kind: 'new' });

  function onCompose(next: Start): void {
    start = next;
    open = true;
  }

  async function onResolve(): Promise<void> {}
  function onDone(): void {}
  function onChanged(): void {}
</script>

<Section id="g-mail" data-gallery="mail" title="Mail">
  <ul class="mail-list mail-list--cards" aria-label="Message cards">
    {#each hits.slice(0, 3) as hit (hit.message_id)}
      <MessageCard {hit} />
    {/each}
  </ul>
  <ul class="mail-list mail-list--list" aria-label="Message rows">
    {#each hits as hit (hit.message_id)}
      <MessageCard {hit} view="list" />
    {/each}
  </ul>
  {#if renewal}
    <pre class="mail-body">{renewal.body}</pre>
    <ul class="mail-files" aria-label="Attachments">
      {#each renewal.attachments as file (file.filename)}
        <li>
          <span>{file.filename}</span>
          <span class="mail-files__meta">{file.content_type}, {sizeText(file.size)}</span>
        </li>
      {/each}
    </ul>
  {/if}
  <div class="mail-actions">
    <Button variant="secondary" onclick={() => onCompose({ kind: 'new' })}>Open a new email</Button>
    <Button
      variant="secondary"
      onclick={() => onCompose({ kind: 'forward', message: 101, subject: 'Seed swap on Saturday' })}
      >Open a forward</Button
    >
  </div>
  <ReceiptLine tone="pending" title="Sending to Odile Marsh in 7 s" onundo={() => undefined} />
  <ReceiptLine
    title="Archived"
    at={Date.parse('2026-11-10T09:30:00Z')}
    undoUntil={Number.MAX_SAFE_INTEGER}
    onundo={() => undefined}
  />
  <Notice tone="warn" title="Mail is in shadow mode">
    atrium only watches the mailbox in shadow mode. Switch Mail to live mode to change messages. Nothing was changed.
  </Notice>
  <Unresolved
    stuck="send"
    title="The send outcome is unknown"
    subject="Email to Odile Marsh"
    onresolve={onResolve}
    ondone={onDone}
  />
  <div class="grid">
    <ViewState kind="loading" title="Loading messages" />
    <ViewState kind="empty" title="No messages yet">Messages show here once atrium has read the mailbox.</ViewState>
    <ViewState kind="error" title="Messages did not load">The mail index could not be read.</ViewState>
    <ViewState kind="offline" title="Cannot reach atrium">
      Messages load when atrium answers again. Nothing from the mailbox is kept on this device.
    </ViewState>
  </div>
  <Compose bind:open {start} accounts={mailAccountsSeed} onchanged={onChanged} />
</Section>
