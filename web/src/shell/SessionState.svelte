<script lang="ts" module>
  import type { SessionView } from './session.svelte';

  export function stateTitle(view: SessionView): string {
    switch (view.status) {
      case 'signed-out':
        return view.after === 'logout' ? 'You are signed out' : 'Sign in to atrium';
      case 'error':
        return view.unavailable ? 'Sign-in is unavailable' : 'Cannot reach atrium';
      case 'redirecting':
        return 'Signing in';
      default:
        return 'Loading';
    }
  }
</script>

<script lang="ts">
  import Button from '../shared/ui/Button.svelte';
  import { loadSession, loginTarget } from './session.svelte';

  let { view }: { view: SessionView } = $props();

  const target = $derived(view.status === 'signed-out' ? loginTarget(view.signIn) : undefined);
  const proxy = $derived(view.status === 'signed-out' && (view.signIn?.modes.includes('proxy') ?? false));
</script>

{#if view.status === 'loading' || view.status === 'redirecting'}
  <p class="frame-note">
    {view.status === 'redirecting'
    ? 'Signing in'
    : 'Loading atrium. Home, your plan and the inbox appear here in a moment.'}
  </p>
{:else}
  <section class="gate">
    <h1 class="gate__title">{stateTitle(view)}</h1>
    {#if view.status === 'signed-out'}
      {#if target}
        <p class="gate__text">
          {view.after === 'logout' ? 'Sign in again to keep working.' : 'Your session has ended or has not started yet.'}
        </p>
      {:else if proxy}
        <p class="gate__text">
          atrium takes your identity from the proxy in front of it. Sign in there, then try again.
        </p>
      {:else}
        <p class="gate__text">This server has no sign-in page. Open atrium from a signed-in browser, then try again.</p>
      {/if}
      <div class="cluster">
        {#if target}
          <a class="btn btn--primary" href={target.href}>Sign in</a>
        {/if}
        <Button variant={target ? 'secondary' : 'primary'} onclick={() => loadSession(false)}>Try again</Button>
      </div>
    {:else}
      <p class="gate__text">
        {view.status === 'error' && view.unavailable
    ? 'atrium cannot check who you are right now.'
    : 'atrium did not answer. Check your connection.'}
      </p>
      <div class="cluster">
        <Button variant="primary" onclick={() => loadSession()}>Try again</Button>
      </div>
    {/if}
  </section>
{/if}
