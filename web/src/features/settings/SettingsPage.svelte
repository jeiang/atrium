<script lang="ts">
  import { on } from 'svelte/events';
  import SettingsForm from '../../shared/settings/SettingsForm.svelte';
  import { useSettingsSchema } from '../../shared/settings/settings';
  import List from '../../shared/ui/List.svelte';
  import ListRow from '../../shared/ui/ListRow.svelte';
  import Page from '../../shared/ui/Page.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';
  import { useShell } from '../../shell/frame';
  import { navigate, p, route } from '../../shell/router';
  import { session } from '../../shell/session.svelte';
  import Backups from './Backups.svelte';
  import Notifications from './Notifications.svelte';
  import { BACKUPS, GROUPS, NOTIFICATIONS } from './placement';
  import Tokens from './Tokens.svelte';
  import './settings.css';

  let phone = $state(false);

  $effect(() => {
    const query = matchMedia('(width < 861px)');
    phone = query.matches;
    return on(query, 'change', () => (phone = query.matches));
  });

  const schema = useSettingsSchema();
  const shell = useShell();
  const slug = $derived(route.params.group);
  const key = $derived(String(route.search.key ?? '') || undefined);
  const pages = [...GROUPS, NOTIFICATIONS, BACKUPS];
  const open = $derived(pages.find((group) => group.slug === (slug ?? 'general')));
  const locked = $derived(
    session.view.status === 'signed-in' && session.view.session.identity.agent
      ? 'This sign-in can read settings but not change them.'
      : undefined,
  );

  $effect(() => {
    if (slug || !key || !schema.data) return;
    const group = schema.data.pages.flatMap((page) => page.fields).find((field) => field.key === key)?.group;
    const target = GROUPS.find((item) => item.id === group);
    if (target) void navigate('/settings/:group', { params: { group: target.slug }, search: { key }, replace: true });
  });
</script>

<Page title="Settings">
  <div class="settings" data-index={slug === undefined || undefined}>
    <nav class="settings__nav" aria-label="Settings pages">
      <List>
        {#each pages as group (group.slug)}
          <ListRow
            title={group.title}
            href={p('/settings/:group', { params: { group: group.slug } })}
            selected={open?.slug === group.slug && (slug !== undefined || !phone)}
          />
        {/each}
        <ListRow title="Connections" href={p('/settings/connections')} />
        <ListRow title="Assistant" meta="Spending, routines and helpers" href={p('/assistant')} />
      </List>
    </nav>
    <div class="settings__panel">
      {#if !open}
        <ViewState kind="empty" title="No such settings page" level={2}>
          <a class="link" href={p('/settings')}>All settings</a>
        </ViewState>
      {:else if open.id === NOTIFICATIONS.id}
        <section class="settings-form" aria-labelledby="notifications-title">
          <div class="settings-form__head">
            {#if phone}
              <a class="link settings__back" href={p('/settings')}>All settings</a>
            {/if}
            <h2 class="settings-form__title" id="notifications-title">{NOTIFICATIONS.title}</h2>
          </div>
          <Notifications {locked} {phone} />
        </section>
      {:else if open.id === BACKUPS.id}
        <section class="settings-form" aria-labelledby="backups-title">
          <div class="settings-form__head">
            {#if phone}
              <a class="link settings__back" href={p('/settings')}>All settings</a>
            {/if}
            <h2 class="settings-form__title" id="backups-title">{BACKUPS.title}</h2>
          </div>
          <Backups {locked} />
        </section>
      {:else}
        {#key open.id}
          <SettingsForm
            id="settings-group"
            groups={[open.id]}
            title={open.title}
            focus={key}
            view={open.id === 'apps'}
            {locked}
            {phone}
          >
            {#snippet head()}
              {#if phone}
                <a class="link settings__back" href={p('/settings')}>All settings</a>
              {/if}
            {/snippet}
            {#snippet intro()}
              {#if open?.id === 'settings.general'}
                <div class="setting">
                  <div class="setting__readonly">
                    <span class="setting__label">Weather location</span>
                    <a
                      class="link"
                      href={p('/settings/connections/:kind/:name', { params: { kind: 'service', name: 'weather' } })}
                    >
                      Set on the weather connection
                    </a>
                  </div>
                </div>
              {:else if open?.id === 'connections'}
                <div class="setting">
                  <div class="setting__readonly">
                    <span class="setting__label">Accounts and keys</span>
                    <a class="link" href={p('/settings/connections')}>Open the connection list</a>
                  </div>
                </div>
              {:else if open?.id === 'apps'}
                <p class="meta">Shown read-only here. A change to an app applies after atrium restarts.</p>
              {:else if open?.id === 'apps.mail'}
                <div class="setting" id="setting-mail_mode" tabindex="-1">
                  <div class="setting__readonly">
                    <span class="setting__label">Mail mode</span>
                    <span class="setting__value">
                      {#if shell.data?.mail}
                        {shell.data.mail.mode === 'live' ? 'Live' : 'Practice (shadow)'}{shell.data.mail.paused ? ', paused' : ''}
                      {:else}
                        Not known
                      {/if}
                    </span>
                    <span class="meta">
                      Changed on the host with <code>atrium settings set mail_mode live</code> or
                      <code>shadow</code>
                    </span>
                  </div>
                </div>
              {/if}
            {/snippet}
          </SettingsForm>
          {#if open.id === 'settings.sign_in'}
            <Tokens {locked} {phone} />
          {/if}
        {/key}
      {/if}
    </div>
  </div>
</Page>
