<script lang="ts">
  import type { Account } from '../../contract';
  import { words } from '../../features/connections/connections';
  import Badge from '../../shared/ui/Badge.svelte';
  import { p } from '../../shell/router';

  let { account }: { account: Account } = $props();

  const state = $derived(words(account.enabled ? account.state : 'disabled'));
</script>

<li class="mail-item-wrap">
  <article class="mail-item" data-pinned={account.pinned || undefined}>
    <header class="mail-item__head">
      <h3 class="mail-item__subject">
        <a
          class="mail-item__link"
          href={p('/settings/connections/:kind/:name', { params: { kind: 'mail', name: account.id } })}
          >{account.label || account.id}</a
        >
      </h3>
      <p class="mail-item__byline"><span class="mail-item__from">{account.login}</span></p>
    </header>
    {#if account.reason && account.state !== 'ok'}
      <p class="mail-decision__outcome">{account.reason}</p>
    {/if}
    <footer class="mail-item__marks">
      <Badge tone={state.tone}>{state.label}</Badge>
      {#if account.pinned}
        <span>Set by the host</span>
      {/if}
      <span class="mail-item__where">{account.enabled ? 'On' : 'Off'}</span>
    </footer>
  </article>
</li>
