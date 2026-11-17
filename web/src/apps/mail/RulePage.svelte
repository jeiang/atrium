<script lang="ts">
  import { useQueryClient } from '@tanstack/svelte-query';
  import type { MailRuleInput } from '../../contract';
  import { plural } from '../../shared/assistant/format';
  import { device, unreachable } from '../../shared/device.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import Dialog from '../../shared/ui/Dialog.svelte';
  import Notice from '../../shared/ui/Notice.svelte';
  import Skeleton from '../../shared/ui/Skeleton.svelte';
  import { toast } from '../../shared/ui/toast.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';
  import { useApps } from '../../shell/manifest';
  import { navigate, p, route } from '../../shell/router';
  import DecisionCard from './DecisionCard.svelte';
  import MailFrame from './MailFrame.svelte';
  import { type Problem, problemOf, useAccounts } from './mail';
  import {
    conditionText,
    handleRuleDelete,
    handleRuleSave,
    type MailListedRule,
    mailEnabled,
    refreshRules,
    ruleSentence,
    ruleTitle,
    useRuleDecisions,
    useRules,
    useVocabulary,
  } from './manage';
  import RuleForm, { type RuleDraft } from './RuleForm.svelte';

  const client = useQueryClient();
  const apps = useApps();
  const ready = $derived(mailEnabled(apps.data));
  const id = $derived(route.params.id ?? '');
  const creating = $derived(id === 'new');

  const rules = useRules(() => ready);
  const vocabulary = useVocabulary(() => ready);
  const accounts = useAccounts(() => ready);
  const decisions = useRuleDecisions(
    () => (creating ? '' : id),
    () => ready,
  );

  const rule = $derived(rules.data?.find((item) => item.id === id));
  const first = $derived(decisions.data?.pages[0]);
  const ref = $derived(first?.rule);
  const items = $derived(decisions.data?.pages.flatMap((page) => page?.items ?? []) ?? []);
  const accountOptions = $derived([
    { value: '', label: 'Every account' },
    ...(accounts.data ?? []).map((item) => ({ value: item.id, label: item.label || item.id })),
  ]);
  const gone = $derived(
    !creating && rules.data !== undefined && !rule && decisions.data !== undefined && first === null,
  );
  const title = $derived(
    creating ? 'New rule' : rule ? ruleTitle(rule) : ref?.title || (ref ? `Rule ${ref.id}` : 'Rule'),
  );

  let editing = $state(false);
  let draft = $state<RuleDraft>({ account: '', action: 'file', dest: '', when: [] });
  let saving = $state(false);
  let problem = $state<Problem>();
  let asking = $state(false);
  let deleting = $state(false);
  let deleteProblem = $state<Problem>();

  const showForm = $derived(creating || editing);

  function draftOf(source: MailListedRule | undefined): RuleDraft {
    if (!source) {
      const field = vocabulary.data?.fields[0];
      return {
        account: '',
        action: 'file',
        dest: '',
        when: [{ key: 0, field: field?.name ?? '', op: field?.ops[0] ?? '', value: '' }],
      };
    }
    return {
      account: source.account ?? '',
      action: source.action,
      dest: source.dest ?? '',
      when: source.when.map((item, key) => ({
        key,
        field: item.field,
        op: item.op,
        value: Array.isArray(item.value) ? item.value.join(', ') : item.value == null ? '' : String(item.value),
      })),
    };
  }

  let seeded = $state('');
  $effect(() => {
    const key = `${id}:${creating ? 'new' : rule ? 'rule' : ''}:${vocabulary.data ? 'v' : ''}`;
    if (key === seeded || !vocabulary.data || (!creating && !rule)) return;
    seeded = key;
    draft = draftOf(creating ? undefined : rule);
  });

  function onEdit(): void {
    draft = draftOf(rule);
    problem = undefined;
    editing = true;
  }

  function onCancel(): void {
    if (creating) {
      void navigate('/apps/mail/rules');
      return;
    }
    editing = false;
    problem = undefined;
  }

  async function onSave(input: MailRuleInput): Promise<void> {
    if (saving) return;
    saving = true;
    problem = undefined;
    try {
      const saved = await handleRuleSave(creating ? undefined : id, input);
      editing = false;
      refreshRules(client);
      toast(creating ? 'Rule created.' : 'Rule saved.');
      if (creating && saved) await navigate('/apps/mail/rules/:id', { params: { id: saved } });
    } catch (error) {
      problem = problemOf(error);
    } finally {
      saving = false;
    }
  }

  async function onDelete(): Promise<void> {
    if (deleting) return;
    deleting = true;
    deleteProblem = undefined;
    try {
      await handleRuleDelete(id);
      asking = false;
      refreshRules(client);
      toast('Rule deleted.');
    } catch (error) {
      deleteProblem = problemOf(error);
    } finally {
      deleting = false;
    }
  }
</script>

<MailFrame {title} current={p('/apps/mail/rules')}>
  <a class="link mail-up" href={p('/apps/mail/rules')}>All rules</a>
  {#if unreachable(rules) || (!device.online && !rules.data)}
    <ViewState kind="offline" title="Cannot reach atrium">
      Rules load when atrium answers again. Nothing from the mailbox is kept on this device.
    </ViewState>
  {:else if (rules.isError && !rules.data) || (decisions.isError && !decisions.data)}
    <ViewState kind="error" title="This rule did not load">
      {(rules.error ?? decisions.error)?.message}
      {#snippet action()}
        <Button
          variant="secondary"
          onclick={() => {
    void rules.refetch();
    void decisions.refetch();
  }}
          >Retry</Button
        >
      {/snippet}
    </ViewState>
  {:else if !rules.data || !vocabulary.data || (!creating && !decisions.data)}
    <Skeleton lines={3} label="Loading the rule" />
  {:else if gone}
    <ViewState kind="empty" title="This rule is gone">
      It was deleted and made no decisions that are still kept.
      {#snippet action()}
        <a class="link" href={p('/apps/mail/rules')}>All rules</a>
      {/snippet}
    </ViewState>
  {:else}
    {#if showForm}
      <RuleForm
        id={creating ? '' : id}
        bind:draft
        vocabulary={vocabulary.data}
        accounts={accountOptions}
        before={rule}
        {saving}
        {problem}
        onsave={(input) => void onSave(input)}
        oncancel={onCancel}
      />
    {:else if rule}
      <section class="mail-rule-detail" aria-label="The rule">
        <p class="mail-rule__sentence mail-rule__sentence--lead">{ruleSentence(rule, vocabulary.data)}</p>
        {#if ref?.state === 'changed'}
          <Notice tone="info" variant="inline" title="Rule changed">
            Older decisions below were made by an earlier version of this rule.
          </Notice>
        {/if}
        <dl class="mail-facts">
          <dt>Applies to</dt>
          <dd>{rule.account ? rule.account : 'Every account'}</dd>
          <dt>Checks</dt>
          <dd>
            <ul class="mail-rule__checks">
              {#each rule.when as condition, index (index)}
                <li>{conditionText(condition, vocabulary.data)}</li>
              {/each}
            </ul>
          </dd>
          <dt>Then</dt>
          <dd>{ruleTitle(rule)}</dd>
        </dl>
        <span class="cluster">
          <Button
            variant="secondary"
            disabled={!device.online}
            reason={device.online ? undefined : 'Cannot reach atrium.'}
            onclick={onEdit}
            >Edit rule</Button
          >
          <Button
            variant="quiet"
            disabled={!device.online}
            reason={device.online ? undefined : 'Cannot reach atrium.'}
            onclick={() => {
    deleteProblem = undefined;
    asking = true;
  }}
            >Delete rule</Button
          >
        </span>
      </section>
    {:else if ref}
      <Notice tone="warn" variant="inline" title="Rule removed">
        Rule {ref.id} was deleted. The decisions it made are kept below.
      </Notice>
    {/if}
    {#if !creating}
      <section class="mail-section" aria-labelledby="rule-decisions">
        <h2 id="rule-decisions" class="mail-section__title">Decisions it made</h2>
        {#if items.length === 0}
          <p class="meta">No decisions yet.</p>
        {:else}
          <p class="meta">{plural(first?.total ?? items.length, 'decision')}, newest first</p>
          <ul class="mail-list mail-list--cards" aria-label="Decisions it made">
            {#each items as decision (decision.decision_id)}
              <DecisionCard {decision} />
            {/each}
          </ul>
          {#if decisions.hasNextPage}
            <span class="cluster">
              <Button
                variant="secondary"
                pending={decisions.isFetchingNextPage}
                onclick={() => void decisions.fetchNextPage()}
                >Show more</Button
              >
            </span>
          {/if}
        {/if}
      </section>
    {/if}
  {/if}
  {#snippet after()}
    <Dialog bind:open={asking} title="Delete this rule?" pending={deleting} initialFocus="cancel">
      {#if rule}
        <p>{ruleSentence(rule, vocabulary.data)}</p>
        <p>
          The rule stops sorting mail now. Its {plural(rule.decision_count, 'past decision')} stay and will say "Rule
          removed". This cannot be undone.
        </p>
      {/if}
      {#if deleteProblem}
        <Notice tone={deleteProblem.tone} variant="inline" title={deleteProblem.title}>{deleteProblem.text}</Notice>
      {/if}
      {#snippet actions()}
        <Button variant="quiet" data-cancel disabled={deleting} onclick={() => (asking = false)}>Cancel</Button>
        <Button variant="danger" pending={deleting} disabled={!device.online} onclick={() => void onDelete()}
          >Delete rule</Button
        >
      {/snippet}
    </Dialog>
  {/snippet}
</MailFrame>
