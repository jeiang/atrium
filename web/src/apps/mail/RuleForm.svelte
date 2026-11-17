<script lang="ts" module>
  import type { MailRuleInput } from '../../contract';

  export interface ConditionDraft {
    key: number;
    field: string;
    op: string;
    value: string;
  }

  export interface RuleDraft {
    account: string;
    action: string;
    dest: string;
    when: ConditionDraft[];
  }
</script>

<script lang="ts">
  import { plural } from '../../shared/assistant/format';
  import { device } from '../../shared/device.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import Notice from '../../shared/ui/Notice.svelte';
  import SelectField from '../../shared/ui/SelectField.svelte';
  import TextField from '../../shared/ui/TextField.svelte';
  import { type Problem, problemOf } from './mail';
  import { handleRulePreview, type MailListedRule, type Preview, type RuleVocabulary, ruleSentence } from './manage';

  interface Props {
    id: string;
    draft: RuleDraft;
    vocabulary: RuleVocabulary;
    accounts: { value: string; label: string }[];
    before: MailListedRule | undefined;
    saving: boolean;
    problem: Problem | undefined;
    onsave: (rule: MailRuleInput) => void;
    oncancel: () => void;
  }

  let { id, draft = $bindable(), vocabulary, accounts, before, saving, problem, onsave, oncancel }: Props = $props();

  let next = $state(1000);
  let preview = $state<Preview>();
  let previewing = $state(false);
  let previewProblem = $state<Problem>();

  const actionOptions = $derived(vocabulary.actions.map((item) => ({ value: item.name, label: item.label })));
  const fieldOptions = $derived(vocabulary.fields.map((item) => ({ value: item.name, label: item.label })));
  const needsDest = $derived(vocabulary.actions.find((item) => item.name === draft.action)?.destination === true);
  const input = $derived(toInput(draft));
  const after = $derived(ruleSentence({ ...input, action: input.action }, vocabulary));

  function opsOf(field: string): { value: string; label: string }[] {
    const names = vocabulary.fields.find((item) => item.name === field)?.ops ?? [];
    return vocabulary.ops.filter((op) => names.includes(op.name)).map((op) => ({ value: op.name, label: op.label }));
  }

  function opOf(name: string) {
    return vocabulary.ops.find((op) => op.name === name);
  }

  function parsedValue(condition: ConditionDraft): unknown {
    const kind = opOf(condition.op)?.value;
    if (kind === 'none') return undefined;
    if (kind === 'list')
      return condition.value
        .split(',')
        .map((item) => item.trim())
        .filter(Boolean);
    if (kind === 'days') return Number(condition.value);
    return condition.value.trim();
  }

  function toInput(value: RuleDraft): MailRuleInput {
    return {
      ...(value.account ? { account: value.account } : {}),
      action: value.action as MailRuleInput['action'],
      ...(needsDest && value.dest.trim() ? { dest: value.dest.trim() } : {}),
      when: value.when.map((item) => {
        const v = parsedValue(item);
        return v === undefined ? { field: item.field, op: item.op } : { field: item.field, op: item.op, value: v };
      }),
      ...(id ? { id } : {}),
    };
  }

  function onField(condition: ConditionDraft): void {
    const ops = opsOf(condition.field);
    if (!ops.some((op) => op.value === condition.op)) condition.op = ops[0]?.value ?? '';
    preview = undefined;
  }

  function onAdd(): void {
    const field = vocabulary.fields[0]?.name ?? '';
    draft.when.push({ key: next++, field, op: opsOf(field)[0]?.value ?? '', value: '' });
    preview = undefined;
  }

  function onRemove(key: number): void {
    draft.when = draft.when.filter((item) => item.key !== key);
    preview = undefined;
  }

  async function onPreview(): Promise<void> {
    if (previewing) return;
    previewing = true;
    previewProblem = undefined;
    try {
      preview = await handleRulePreview(input);
    } catch (error) {
      preview = undefined;
      previewProblem = problemOf(error);
    } finally {
      previewing = false;
    }
  }

  function onSubmit(event: SubmitEvent): void {
    event.preventDefault();
    if (saving) return;
    onsave(input);
  }
</script>

<form class="mail-rule-form" onsubmit={onSubmit} aria-label={id ? 'Edit rule' : 'New rule'}>
  <SelectField label="Applies to" options={accounts} bind:value={draft.account} />
  <fieldset class="mail-rule-form__when">
    <legend>When all of these are true</legend>
    {#each draft.when as condition, index (condition.key)}
      <div class="mail-rule-form__row">
        <SelectField
          label="Part {index + 1}"
          options={fieldOptions}
          bind:value={condition.field}
          onchange={() => onField(condition)}
        />
        <SelectField
          label="Test {index + 1}"
          options={opsOf(condition.field)}
          bind:value={condition.op}
          onchange={() => (preview = undefined)}
        />
        {#if opOf(condition.op)?.value !== 'none'}
          <TextField
            label="Value {index + 1}"
            help={opOf(condition.op)?.hint || undefined}
            bind:value={condition.value}
            oninput={() => (preview = undefined)}
            required
          />
        {/if}
        <Button variant="quiet" icon="close" label="Remove part {index + 1}" onclick={() => onRemove(condition.key)} />
      </div>
    {/each}
    <Button variant="secondary" icon="plus" onclick={onAdd}>Add a condition</Button>
  </fieldset>
  <SelectField label="Then" options={actionOptions} bind:value={draft.action} onchange={() => (preview = undefined)} />
  {#if needsDest}
    <TextField label="Folder" bind:value={draft.dest} required oninput={() => (preview = undefined)} />
  {/if}
  <section class="mail-compare" aria-label="Before and after">
    <div class="mail-compare__side">
      <h3 class="eyebrow">Before</h3>
      {#if before}
        <p>{ruleSentence(before, vocabulary)}</p>
        <p class="meta">{plural(before.decision_count, 'decision')} so far</p>
      {:else}
        <p>No rule yet.</p>
      {/if}
    </div>
    <div class="mail-compare__side">
      <h3 class="eyebrow">After</h3>
      <p>{after}</p>
      {#if preview}
        <p class="meta" role="status">
          Matches {plural(preview.count, 'message')} of the last {preview.total}, {preview.in_inbox} in the inbox now.
        </p>
        {#if preview.sample.length > 0}
          <ul class="mail-compare__sample" aria-label="Messages it would catch">
            {#each preview.sample as item (item.id)}
              <li>
                <span>{item.subject || 'No subject'}</span>
                <span class="meta">{item.from_name || item.from_addr}, {item.folder}</span>
              </li>
            {/each}
          </ul>
        {/if}
      {/if}
      {#if previewProblem}
        <Notice tone={previewProblem.tone} variant="inline" title={previewProblem.title}>{previewProblem.text}</Notice>
      {/if}
      <Button
        variant="secondary"
        pending={previewing}
        disabled={!device.online || draft.when.length === 0}
        reason={device.online ? undefined : 'Cannot reach atrium.'}
        onclick={() => void onPreview()}
        >Preview matches</Button
      >
    </div>
  </section>
  {#if problem}
    <Notice tone={problem.tone} variant="inline" title={problem.title}>{problem.text}</Notice>
  {/if}
  <span class="cluster">
    <Button
      type="submit"
      variant="primary"
      pending={saving}
      disabled={!device.online || draft.when.length === 0}
      reason={device.online ? undefined : 'Cannot reach atrium.'}
      >Save rule</Button
    >
    <Button variant="quiet" disabled={saving} onclick={oncancel}>Cancel</Button>
  </span>
</form>
