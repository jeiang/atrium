<script lang="ts">
  import { formatDayTime } from '../../shared/assistant/format';
  import Icon from '../../shared/ui/Icon.svelte';
  import { p } from '../../shell/router';
  import { type MailSearchHit, sender } from './mail';

  let { hit, view = 'cards' }: { hit: MailSearchHit; view?: 'cards' | 'list' } = $props();

  const href = $derived(p('/apps/mail/message/:id', { params: { id: String(hit.message_id) } }));
  const who = $derived(sender(hit.from));
  const when = $derived(formatDayTime(hit.date));
</script>

<li class="mail-item mail-item--{view}" data-unread={!hit.seen || undefined}>
  <a class="mail-item__link" {href}>
    <span class="mail-item__head">
      {#if !hit.seen}
        <span class="mail-item__dot"></span><span class="visually-hidden">Unread,</span>
      {/if}
      <span class="mail-item__from">{who}</span>
      <time class="mail-item__date" datetime={hit.date}>{when}</time>
    </span>
    <span class="mail-item__subject">{hit.subject || 'No subject'}</span>
    {#if view === 'cards' && hit.snippet}
      <span class="mail-item__snippet">{hit.snippet}</span>
    {/if}
  </a>
  <span class="mail-item__marks">
    {#if hit.flagged}
      <span class="mail-item__mark" title="Flagged"><Icon name="flag" /><span class="visually-hidden">Flagged</span></span>
    {/if}
    {#if hit.has_attachment}
      <span class="mail-item__mark" title="Has attachments"
        ><Icon name="attachment" /><span class="visually-hidden">Has attachments</span></span
      >
    {/if}
    <span class="mail-item__where">{hit.account} · {hit.folder}</span>
  </span>
</li>
