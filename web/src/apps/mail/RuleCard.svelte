<script lang="ts">
  import { plural } from '../../shared/assistant/format';
  import { type MailListedRule, type RuleVocabulary, rulePath, ruleSentence, ruleTitle } from './manage';

  let { rule, vocabulary }: { rule: MailListedRule; vocabulary: RuleVocabulary | undefined } = $props();
</script>

<li class="mail-item-wrap">
  <article class="mail-item mail-rule">
    <h3 class="mail-item__subject">
      <a class="mail-item__link" href={rulePath(rule.id)}>{ruleTitle(rule)}</a>
    </h3>
    <p class="mail-rule__sentence">{ruleSentence(rule, vocabulary)}</p>
    <footer class="mail-item__marks">
      <span>{rule.decision_count === 0 ? 'No decisions yet' : plural(rule.decision_count, 'decision')}</span>
      <span class="mail-item__where">{rule.account ? `Only ${rule.account}` : 'Every account'}</span>
    </footer>
  </article>
</li>
