<script lang="ts">
  import { formatDayTime } from '../../shared/assistant/format';
  import Icon from '../../shared/ui/Icon.svelte';
  import { p } from '../../shell/router';
  import { type MailSearchHit, sender } from './mail';

  let { hit, view = 'cards' }: { hit: MailSearchHit; view?: 'cards' | 'list' } = $props();

  const href = $derived(p('/apps/mail/message/:id', { params: { id: String(hit.message_id) } }));
</script>

<li class="mail-item-wrap">
  <article class="mail-item mail-item--{view}" data-unread={!hit.seen || undefined}>
    <header class="mail-item__head">
      <h3 class="mail-item__subject">
        <a class="mail-item__link" {href}>
          {#if !hit.seen}
            <span class="mail-item__dot"></span><span class="visually-hidden">Unread:</span>
          {/if}
          {hit.subject || 'No subject'}
        </a>
      </h3>
      <p class="mail-item__byline">
        <span class="mail-item__from">{sender(hit.from)}</span>
        <time class="mail-item__date" datetime={hit.date}>{formatDayTime(hit.date)}</time>
      </p>
    </header>
    {#if view === 'cards' && hit.snippet}
      <p class="mail-item__snippet">{hit.snippet}</p>
    {/if}
    <footer class="mail-item__marks">
      {#if hit.flagged}
        <span class="mail-item__mark"><Icon name="flag" />Flagged</span>
      {/if}
      {#if hit.has_attachment}
        <span class="mail-item__mark"><Icon name="attachment" />Attachment</span>
      {/if}
      <span class="mail-item__where">{hit.folder} in {hit.account}</span>
    </footer>
  </article>
</li>
