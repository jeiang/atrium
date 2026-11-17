<script lang="ts">
  import AccountRow from '../../apps/mail/AccountRow.svelte';
  import CommandDialog from '../../apps/mail/CommandDialog.svelte';
  import DecisionCard from '../../apps/mail/DecisionCard.svelte';
  import type { Command } from '../../apps/mail/manage';
  import RuleCard from '../../apps/mail/RuleCard.svelte';
  import RuleForm, { type RuleDraft } from '../../apps/mail/RuleForm.svelte';
  import StatusView from '../../apps/mail/StatusView.svelte';
  import '../../apps/mail/mail.css';
  import { mailAccountsSeed } from '../../mocks/mail';
  import { mailVocabulary, seedDecisions, seedRules, seedStatus } from '../../mocks/mailManage';
  import Button from '../../shared/ui/Button.svelte';
  import Section from '../../shared/ui/Section.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';

  const rules = seedRules();
  const decisions = seedDecisions();
  const bills = rules[0];
  const checks = decisions.filter((row) => row.agreement === 'disagree' || row.agreement === 'pending');
  const accounts = [
    { value: '', label: 'Every account' },
    { value: 'garden-club', label: 'Garden club' },
  ];
  let draft = $state<RuleDraft>({
    account: '',
    action: 'file',
    dest: 'Bills',
    when: [{ key: 0, field: 'from_domain', op: 'suffix', value: 'meadow-water.example, hillside-power.example' }],
  });
  let command = $state<Command>('go_live');
  let open = $state(false);

  function onAsk(next: Command): void {
    command = next;
    open = true;
  }
</script>

<Section id="g-mail-manage" data-gallery="mail-manage" title="Mail rules and mode">
  <ol class="mail-list" aria-label="Rule cards">
    {#each rules as rule, index (rule.id)}
      <RuleCard
        {rule}
        vocabulary={mailVocabulary}
        position={index + 1}
        total={rules.length}
        moving={undefined}
        blocked={false}
        onmove={() => undefined}
      />
    {/each}
  </ol>
  <RuleForm
    id="bills"
    bind:draft
    vocabulary={mailVocabulary}
    {accounts}
    before={bills}
    saving={false}
    problem={undefined}
    onsave={() => undefined}
    oncancel={() => undefined}
  />
  <ul class="mail-list mail-list--cards" aria-label="Decision cards">
    {#each decisions.slice(0, 4) as decision (decision.decision_id)}
      <DecisionCard {decision} />
    {/each}
  </ul>
  <div class="mail-section" data-gallery-status="shadow">
    <StatusView status={seedStatus('shadow')} checks={undefined} onask={onAsk} oncheck={() => undefined} />
  </div>
  <div class="mail-section" data-gallery-status="ineligible">
    <StatusView status={seedStatus('ineligible')} checks={undefined} onask={onAsk} oncheck={() => undefined} />
  </div>
  <div class="mail-section" data-gallery-status="watch">
    <StatusView status={seedStatus('watch')} {checks} onask={onAsk} oncheck={() => undefined} />
  </div>
  <div class="mail-section" data-gallery-status="blocked">
    <StatusView
      status={seedStatus('live')}
      checks={undefined}
      blocked="go_live"
      onask={onAsk}
      oncheck={() => undefined}
    />
  </div>
  <div class="mail-actions">
    <Button variant="secondary" onclick={() => onAsk('go_live')}>Open the go-live confirm</Button>
    <Button variant="secondary" onclick={() => onAsk('replay')}>Open the replay confirm</Button>
  </div>
  <ul class="mail-list" aria-label="Account rows">
    {#each mailAccountsSeed as account (account.id)}
      <AccountRow {account} />
    {/each}
  </ul>
  <div class="grid">
    <ViewState kind="loading" title="Loading rules" />
    <ViewState kind="error" title="The mail status did not load">The mail status could not be read.</ViewState>
    <ViewState kind="offline" title="Cannot reach atrium">
      The mail mode loads when atrium answers again. Nothing from it is kept on this device.
    </ViewState>
  </div>
  <CommandDialog {command} bind:open ondone={() => undefined} onunknown={() => undefined} />
</Section>
