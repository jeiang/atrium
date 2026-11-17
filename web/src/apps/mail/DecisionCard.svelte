<script lang="ts">
  import { formatDayTime } from '../../shared/assistant/format';
  import { p } from '../../shell/router';
  import { agreementWords, type Decision, outcomeOf, stageWords } from './manage';
  import RuleLink from './RuleLink.svelte';

  let { decision }: { decision: Decision } = $props();

  const href = $derived(p('/apps/mail/message/:id', { params: { id: String(decision.message_id) } }));
  const outcome = $derived(outcomeOf(decision.action, decision.dest));
</script>

<li class="mail-item-wrap">
  <article class="mail-item">
    <header class="mail-item__head">
      <h3 class="mail-item__subject">
        <a class="mail-item__link" {href}>{decision.subject || 'No subject'}</a>
      </h3>
      <p class="mail-item__byline">
        <span class="mail-item__from">{decision.from_name || decision.from_addr}</span>
        <time class="mail-item__date" datetime={new Date(decision.date_ts * 1000).toISOString()}
          >{formatDayTime(decision.date_ts)}</time
        >
      </p>
    </header>
    <p class="mail-decision__outcome">
      {#if decision.corrected_to}
        <del>{outcome}</del> <span>You moved it to {decision.corrected_to}</span>
      {:else}
        {outcome}
      {/if}
    </p>
    <footer class="mail-item__marks">
      <span>{stageWords[decision.stage] ?? decision.stage}</span>
      <span>{agreementWords[decision.agreement] ?? decision.agreement}</span>
      {#if decision.rule}
        <RuleLink rule={decision.rule} />
      {/if}
      <span class="mail-item__where">{decision.account}</span>
    </footer>
  </article>
</li>
