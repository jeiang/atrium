<script lang="ts">
  import { plural } from '../../shared/assistant/format';
  import Button from '../../shared/ui/Button.svelte';
  import { type MailListedRule, type RuleVocabulary, rulePath, ruleSentence, ruleTitle } from './manage';

  let {
    rule,
    vocabulary,
    position,
    total,
    moving,
    blocked,
    onmove,
  }: {
    rule: MailListedRule;
    vocabulary: RuleVocabulary | undefined;
    position: number;
    total: number;
    moving: 'up' | 'down' | undefined;
    blocked: boolean;
    onmove: (direction: 'up' | 'down') => void;
  } = $props();

  const title = $derived(ruleTitle(rule));
</script>

<li class="mail-item-wrap">
  <article class="mail-item mail-rule">
    <h3 class="mail-item__subject mail-rule__head">
      <span class="mail-rule__position" aria-hidden="true">{position}</span>
      <a class="mail-item__link" href={rulePath(rule.id)}>{title}</a>
    </h3>
    <p class="mail-rule__sentence">{ruleSentence(rule, vocabulary)}</p>
    <footer class="mail-item__marks">
      <span>{rule.decision_count === 0 ? 'No decisions yet' : plural(rule.decision_count, 'decision')}</span>
      <span class="mail-item__where">{rule.account ? `Only ${rule.account}` : 'Every account'}</span>
    </footer>
    {#if total > 1}
      <span class="cluster mail-rule__move">
        <Button
          size="sm"
          variant="quiet"
          data-move="{rule.id}:up"
          label="Move rule {position}, {title}, up"
          pending={moving === 'up'}
          disabled={position === 1 || blocked}
          onclick={() => onmove('up')}
          >Up</Button
        >
        <Button
          size="sm"
          variant="quiet"
          data-move="{rule.id}:down"
          label="Move rule {position}, {title}, down"
          pending={moving === 'down'}
          disabled={position === total || blocked}
          onclick={() => onmove('down')}
          >Down</Button
        >
      </span>
    {/if}
  </article>
</li>
