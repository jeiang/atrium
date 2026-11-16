<script lang="ts">
  import { useQueryClient } from '@tanstack/svelte-query';
  import { assistantDefaultsSet, ok } from '../../contract';
  import { unreachable } from '../../shared/device.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import Notice from '../../shared/ui/Notice.svelte';
  import Page from '../../shared/ui/Page.svelte';
  import Section from '../../shared/ui/Section.svelte';
  import SelectField from '../../shared/ui/SelectField.svelte';
  import Skeleton from '../../shared/ui/Skeleton.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';
  import { p } from '../../shell/router';
  import AssistantNav from './AssistantNav.svelte';
  import { failure, fieldErrors, gesture, type Outcome, refresh, useConfiguration, useRoles } from './assistant';
  import SettingsGroupForm from './SettingsGroupForm.svelte';
  import './assistant.css';

  const client = useQueryClient();
  const configuration = useConfiguration();
  const roles = useRoles();
  const offline = $derived(unreachable(configuration));
  const rolesOffline = $derived(unreachable(roles));

  let draft = $state({ model: '', effort: '', context: '' });
  let synced = $state<string>();
  let pending = $state(false);
  let errors = $state<Record<string, string>>({});
  let result = $state<Outcome>();

  const config = $derived(configuration.data);
  const base = $derived({
    model: config?.defaults.model ?? '',
    effort: config?.defaults.effort ?? '',
    context: String(config?.defaults.context_limit ?? ''),
  });
  const signature = $derived(`${base.model}|${base.effort}|${base.context}`);
  const dirty = $derived(draft.model !== base.model || draft.effort !== base.effort || draft.context !== base.context);
  const chosen = $derived(config?.models.find((model) => model.id === draft.model));
  const modelOptions = $derived(config?.models.map((model) => ({ value: model.id, label: model.name })) ?? []);
  const effortOptions = $derived(
    (chosen?.efforts.length ? chosen.efforts : (config?.efforts ?? [])).map((effort) => ({
      value: effort,
      label: effort === 'xhigh' ? 'Extra high' : effort.charAt(0).toUpperCase() + effort.slice(1),
    })),
  );
  const contextOptions = $derived(
    (config?.context_limits ?? []).map((limit) => ({ value: String(limit), label: tokens(limit) })),
  );
  const price = $derived(
    chosen
      ? `$${chosen.rates.input} per million input tokens and $${chosen.rates.output} per million output tokens. One run stops at $${chosen.run_cap_usd}.`
      : '',
  );

  $effect(() => {
    if (!config || signature === synced) return;
    if (synced === undefined || !dirty) draft = { ...base };
    synced = signature;
  });

  function tokens(limit: number): string {
    return limit >= 1_000_000 ? `${limit / 1_000_000} million tokens` : `${Math.round(limit / 1000)}k tokens`;
  }

  async function onSave(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    if (!config || !dirty || pending) return;
    pending = true;
    result = undefined;
    errors = {};
    try {
      ok(
        await assistantDefaultsSet(
          { model: draft.model, effort: draft.effort, context_limit: Number(draft.context) },
          gesture(),
        ),
      );
      result = { tone: 'ok', title: 'Defaults saved', text: 'New chats start with them. Open chats keep their own.' };
      refresh(client, 'settings');
      refresh(client, 'assistant');
    } catch (error) {
      errors = fieldErrors(error);
      result = failure(error, 'the defaults', () => void configuration.refetch());
    } finally {
      pending = false;
    }
  }
</script>

<Page
  title="Behavior"
  eyebrow="Assistant"
  lede="How the assistant works when you start a chat, and which helpers it uses."
>
  <AssistantNav current="behavior" />
  <div class="asst">
    <Section id="defaults" title="Defaults for new chats">
      {#if offline}
        <ViewState kind="offline" title="Cannot reach atrium" level={3}
          >The defaults load when atrium answers again.</ViewState
        >
      {:else if configuration.isError && !config}
        <ViewState kind="error" title="The defaults did not load" level={3}>
          {configuration.error.message}
          {#snippet action()}
            <Button variant="secondary" onclick={() => void configuration.refetch()}>Retry</Button>
          {/snippet}
        </ViewState>
      {:else if !config}
        <Skeleton lines={3} label="Loading the defaults" />
      {:else if config.models.length === 0}
        <ViewState kind="empty" title="No models are available" level={3}>
          atrium has no model it can use. Check the inference connection in Settings.
        </ViewState>
      {:else}
        <form class="stack asst-form" novalidate onsubmit={onSave} data-settings="defaults">
          <SelectField
            label="Model"
            bind:value={draft.model}
            options={modelOptions}
            help={price}
            error={errors.model}
          />
          <SelectField label="Effort" bind:value={draft.effort} options={effortOptions} error={errors.effort} />
          <SelectField
            label="Context limit"
            bind:value={draft.context}
            options={contextOptions}
            help="Longer context costs more per message."
            error={errors.context_limit}
          />
          <div class="cluster">
            <Button type="submit" variant="primary" {pending} disabled={!dirty} reason="Nothing changed"
              >Save defaults</Button
            >
          </div>
          {#if result}
            <Notice tone={result.tone} variant="inline" title={result.title}>{result.text}</Notice>
          {/if}
        </form>
      {/if}
    </Section>

    <Section id="instructions" title="Standing instructions">
      <p class="asst-text">
        The assistant reads your standing instructions before every run. They live in Knowledge, with their history.
      </p>
      <div class="cluster">
        <a class="asst-link" href={p('/knowledge/instructions')}>Open standing instructions</a>
      </div>
    </Section>

    <SettingsGroupForm
      id="helpers"
      title="Helpers"
      groups={['assistant.behavior', 'assistant.helpers']}
      saveLabel="Save helper settings"
      savedText="The next research or web search run uses them."
      empty="atrium offers no helper settings right now."
    />

    <Section id="roles" title="Research roles">
      <p class="asst-text">The helpers the assistant can start, as declared in atrium. You cannot change them here.</p>
      {#if rolesOffline}
        <ViewState kind="offline" title="Cannot reach atrium" level={3}
          >The roles load when atrium answers again.</ViewState
        >
      {:else if roles.isError && !roles.data}
        <ViewState kind="error" title="The roles did not load" level={3}>
          {roles.error.message}
          {#snippet action()}
            <Button variant="secondary" onclick={() => void roles.refetch()}>Retry</Button>
          {/snippet}
        </ViewState>
      {:else if !roles.data}
        <Skeleton lines={3} label="Loading the roles" />
      {:else if roles.data.length === 0}
        <ViewState kind="empty" title="No roles" level={3}>The assistant starts no helpers.</ViewState>
      {:else}
        <ul class="asst-roles" aria-label="Research roles">
          {#each roles.data as role (role.name)}
            <li class="asst-role" data-role={role.name}>
              <h3 class="asst-role__name">{role.name}</h3>
              <p class="asst-text">{role.description}</p>
              <p class="asst-meta">
                Effort {role.effort}. Tools: {role.tools.length > 0 ? role.tools.join(', ') : 'none'}.
              </p>
              <details class="asst-details">
                <summary>Instructions</summary>
                <p class="asst-pre">{role.prompt}</p>
              </details>
            </li>
          {/each}
        </ul>
      {/if}
    </Section>
  </div>
</Page>
