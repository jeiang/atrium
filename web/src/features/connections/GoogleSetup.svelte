<script lang="ts">
  import { useQueryClient } from '@tanstack/svelte-query';
  import {
    ApiError,
    type ConnectionView,
    connectionsOauthComplete,
    connectionsOauthConfigure,
    connectionsOauthStart,
    ok,
  } from '../../contract';
  import { outcomeUnknown, refusedOffline, unreachable } from '../../shared/device.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import Notice from '../../shared/ui/Notice.svelte';
  import RadioGroup from '../../shared/ui/RadioGroup.svelte';
  import Section from '../../shared/ui/Section.svelte';
  import Skeleton from '../../shared/ui/Skeleton.svelte';
  import TextField from '../../shared/ui/TextField.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';
  import { navigate, route } from '../../shell/router';
  import {
    GOOGLE_ID,
    gesture,
    normalized,
    refreshConnections,
    remember,
    splitId,
    useGoogleSetup,
    when,
  } from './connections';
  import SecretField from './SecretField.svelte';

  type Outcome = { tone: 'ok' | 'warn' | 'error'; title?: string; text: string };
  type Busy = 'save' | 'start' | 'finish';

  let { view }: { view: ConnectionView | undefined } = $props();

  const client = useQueryClient();
  const setup = useGoogleSetup();
  const offline = $derived(unreachable(setup));
  const returned = $derived.by(() => {
    const state = String(route.search.state ?? '');
    if (!state) return undefined;
    return { state, code: String(route.search.code ?? ''), error: String(route.search.error ?? '') };
  });

  let clientId = $state('');
  let clientSecret = $state('');
  let capability = $state('calendar');
  let synced = $state(false);
  let busy = $state<Busy>();
  let saved = $state<Outcome>();
  let started = $state<Outcome>();
  let finished = $state<Outcome>();
  let handled = $state<string>();

  const data = $derived(setup.data);
  const ready = $derived(data !== undefined && data.client_id !== '' && data.client_secret_set);
  const dirty = $derived(
    data !== undefined && ((clientId.trim() !== '' && clientId.trim() !== data.client_id) || clientSecret !== ''),
  );
  const grants = $derived(
    (data?.capabilities ?? []).map((grant) => {
      const stored = view?.credentials.find((credential) => credential.slot === grant.slot && credential.present);
      return {
        value: grant.name,
        label: grant.label,
        help: stored ? `Connected ${when(stored.set_at)}. Connecting again replaces it.` : 'Not connected.',
      };
    }),
  );

  $effect(() => {
    if (!data || synced) return;
    clientId = data.client_id;
    synced = true;
  });

  function failure(error: unknown, action: string, reload: () => void): Outcome {
    if (refusedOffline(error) && error instanceof Error) return { tone: 'warn', text: error.message };
    if (outcomeUnknown(error)) {
      reload();
      return {
        tone: 'error',
        title: 'The outcome is unknown',
        text: `${error.message} atrium reloaded Google and did not try again.`,
      };
    }
    if (error instanceof ApiError) {
      return { tone: 'error', title: `atrium refused to ${action}`, text: error.message };
    }
    reload();
    return {
      tone: 'error',
      title: 'The outcome is unknown',
      text: 'The connection failed before atrium answered. atrium reloaded Google and did not try again.',
    };
  }

  function reload(): void {
    void setup.refetch();
    refreshConnections(client);
  }

  function leaveReturn(state: string): void {
    handled = state;
    void navigate('/settings/connections/:kind/:name', { params: splitId(GOOGLE_ID), replace: true });
  }

  async function onSave(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    if (!data || !dirty || busy) return;
    busy = 'save';
    saved = undefined;
    const body: { client_id?: string; client_secret?: string } = {};
    if (clientId.trim() !== '' && clientId.trim() !== data.client_id) body.client_id = clientId.trim();
    if (clientSecret !== '') body.client_secret = clientSecret;
    try {
      remember(client, normalized(ok(await connectionsOauthConfigure(body, gesture())).data));
      clientId = clientId.trim();
      clientSecret = '';
      saved = { tone: 'ok', title: 'Client details saved', text: 'Choose what to connect, then continue to Google.' };
      reload();
    } catch (error) {
      saved = failure(error, 'save the client details', reload);
    } finally {
      busy = undefined;
    }
  }

  async function onStart(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    if (!ready || busy) return;
    busy = 'start';
    started = undefined;
    try {
      const consent = ok(await connectionsOauthStart({ capability }, gesture())).data;
      location.assign(consent.authorize_url);
    } catch (error) {
      started = failure(error, 'start the consent', reload);
      busy = undefined;
    }
  }

  async function onFinish(): Promise<void> {
    const back = returned;
    if (!back || busy) return;
    busy = 'finish';
    finished = undefined;
    try {
      const grant = ok(await connectionsOauthComplete({ state: back.state, code: back.code }, gesture())).data;
      finished = {
        tone: 'ok',
        title: `Connected ${grant.label}`,
        text: 'atrium stored the access Google gave it. Test now checks that it works.',
      };
      reload();
      leaveReturn(back.state);
    } catch (error) {
      finished = failure(error, 'finish connecting Google', reload);
      if (!(refusedOffline(error) && error instanceof Error)) leaveReturn(back.state);
    } finally {
      busy = undefined;
    }
  }

  function onDismiss(): void {
    if (returned) leaveReturn(returned.state);
  }

  function onRetry(): void {
    void setup.refetch();
  }
</script>

<Section id="google-setup" title="Google setup">
  {#if returned && returned.state !== handled}
    <div class="stack conn-return">
      {#if returned.error}
        <Notice tone="error" variant="inline" title="Google did not give consent">
          Google answered {returned.error}. Nothing was connected. Continue to Google again when you are ready.
          {#snippet action()}
            <Button variant="secondary" onclick={onDismiss}>Dismiss</Button>
          {/snippet}
        </Notice>
      {:else}
        <h3 class="conn-step" id="google-return">Finish connecting</h3>
        <p>Google sent you back with its answer. Finish to store the access it gave.</p>
        <div class="cluster">
          <Button
            variant="primary"
            pending={busy === 'finish'}
            disabled={busy !== undefined && busy !== 'finish'}
            onclick={onFinish}
          >
            Finish connecting
          </Button>
          <Button disabled={busy === 'finish'} onclick={onDismiss}>Dismiss</Button>
        </div>
      {/if}
    </div>
  {/if}
  {#if finished}
    <Notice tone={finished.tone} variant="inline" title={finished.title}>{finished.text}</Notice>
  {/if}
  {#if offline}
    <ViewState kind="offline" title="Cannot reach atrium">Google setup loads when atrium answers again.</ViewState>
  {:else if setup.isError && !data}
    <ViewState kind="error" title="Google setup did not load">
      {setup.error.message}
      {#snippet action()}
        <Button variant="secondary" onclick={onRetry}>Retry</Button>
      {/snippet}
    </ViewState>
  {:else if !data}
    <Skeleton lines={6} label="Loading Google setup" />
  {:else}
    <ol class="conn-steps">
      <li class="stack">
        <h3 class="conn-step">Create the Google client</h3>
        <p>
          In Google Cloud, enable the Calendar API, then create an OAuth client of type Web application with this
          redirect address.
        </p>
        <TextField label="Authorised redirect address" value={data.redirect_uri} readonly spellcheck="false" />
      </li>
      <li>
        <form class="stack conn-form" novalidate aria-labelledby="google-client" onsubmit={onSave}>
          <h3 class="conn-step" id="google-client">Client details</h3>
          <TextField
            label="Client ID"
            bind:value={clientId}
            autocomplete="off"
            autocapitalize="none"
            spellcheck="false"
          />
          <SecretField
            label="Client secret"
            bind:value={clientSecret}
            help={data.client_secret_set ? 'Stored. A new value replaces it.' : 'Not set.'}
          />
          <div class="cluster">
            <Button
              type="submit"
              variant={ready ? 'secondary' : 'primary'}
              pending={busy === 'save'}
              disabled={!dirty || (busy !== undefined && busy !== 'save')}
              reason={dirty ? undefined : 'Nothing changed'}
            >
              Save client details
            </Button>
          </div>
          {#if saved}
            <Notice tone={saved.tone} variant="inline" title={saved.title}>{saved.text}</Notice>
          {/if}
        </form>
      </li>
      <li>
        <form class="stack conn-form" novalidate aria-labelledby="google-consent" onsubmit={onStart}>
          <h3 class="conn-step" id="google-consent">Consent</h3>
          <RadioGroup legend="Connect" options={grants} bind:value={capability} />
          <div class="cluster">
            <Button
              type="submit"
              variant={ready ? 'primary' : 'secondary'}
              pending={busy === 'start'}
              disabled={!ready || (busy !== undefined && busy !== 'start')}
              reason={ready ? undefined : 'Save the client ID and secret first'}
            >
              Continue to Google
            </Button>
          </div>
          {#if started}
            <Notice tone={started.tone} variant="inline" title={started.title}>{started.text}</Notice>
          {/if}
        </form>
      </li>
    </ol>
  {/if}
</Section>
