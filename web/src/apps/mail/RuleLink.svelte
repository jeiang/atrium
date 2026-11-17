<script lang="ts">
  import { type RuleRef, rulePath } from './manage';

  let { rule }: { rule: RuleRef } = $props();

  const title = $derived(rule.title || rule.id);
</script>

<span class="mail-rule-ref" data-state={rule.state}>
  {#if rule.state === 'removed'}
    <span>Rule {rule.id}</span>
    <span class="mail-rule-ref__state">Rule removed</span>
  {:else}
    <a class="link" href={rulePath(rule.id)}>Rule: {title}</a>
    {#if rule.state === 'changed'}
      <span class="mail-rule-ref__state">Rule changed</span>
    {/if}
  {/if}
</span>
