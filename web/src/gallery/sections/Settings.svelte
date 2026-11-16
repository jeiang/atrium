<script lang="ts">
  import type { Setting, SettingsSchemaField } from '../../contract';
  import SettingControl from '../../shared/settings/SettingControl.svelte';
  import '../../shared/settings/settings.css';
  import Section from '../../shared/ui/Section.svelte';

  type Sample = {
    name: string;
    field: SettingsSchemaField;
    setting: Setting;
    error?: string;
    elsewhere?: string;
    view?: boolean;
  };

  function field(over: Partial<SettingsSchemaField> & Pick<SettingsSchemaField, 'key' | 'label'>): SettingsSchemaField {
    return {
      module: 'core',
      type: 'string',
      multi: false,
      choices: [],
      default: '',
      value: '',
      source: 'default',
      class: 'operator',
      editable: true,
      secret: false,
      group: 'settings.general',
      restart: false,
      ...over,
    };
  }

  function setting(key: string, value: string, source = 'db', pin?: string): Setting {
    return { key, class: 'operator', value, source, ...(pin === undefined ? {} : { pin_source: pin }) };
  }

  const samples: Sample[] = [
    {
      name: 'text',
      field: field({ key: 'g_tz', label: 'Time zone', default: 'UTC', restart: true, help: 'An IANA zone name.' }),
      setting: setting('g_tz', 'Europe/London'),
    },
    {
      name: 'number with an error',
      field: field({ key: 'g_cap', label: 'Automatic write cap', type: 'int', default: '100' }),
      setting: setting('g_cap', '100', 'default'),
      error: 'must be at most 1000',
    },
    {
      name: 'changed elsewhere',
      field: field({ key: 'g_days', label: 'Sign out after (days)', type: 'int', default: '30' }),
      setting: setting('g_days', '14'),
      elsewhere: '21',
    },
    {
      name: 'pinned by an environment variable',
      field: field({ key: 'g_env', label: 'Account' }),
      setting: setting('g_env', 'family', 'pin', 'env ATRIUM_CALENDAR_ACCOUNT'),
    },
    {
      name: 'pinned in a host file',
      field: field({ key: 'g_host', label: 'Calendar' }),
      setting: setting('g_host', 'Home', 'pin', 'host /etc/atrium/host.toml calendar.icloud_calendar'),
    },
    {
      name: 'switch',
      field: field({ key: 'g_bool', label: 'Send reminders', type: 'bool', default: 'false' }),
      setting: setting('g_bool', 'true'),
    },
    {
      name: 'segmented',
      field: field({
        key: 'g_seg',
        label: 'Reply style',
        type: 'enum',
        default: 'short',
        choices: [
          { value: 'short', label: 'Short' },
          { value: 'full', label: 'Full' },
        ],
      }),
      setting: setting('g_seg', 'short', 'default'),
    },
    {
      name: 'choices',
      field: field({
        key: 'g_multi',
        label: 'Include',
        multi: true,
        default: 'pii,messages',
        choices: [
          { value: 'pii', label: 'Personal details' },
          { value: 'messages', label: 'Messages' },
          { value: 'personal', label: 'Personal notes' },
        ],
      }),
      setting: setting('g_multi', 'pii', 'db'),
    },
    {
      name: 'read-only state',
      field: field({ key: 'g_view', label: 'Mail app is on', type: 'bool', default: 'false', restart: true }),
      setting: setting('g_view', 'true'),
      view: true,
    },
  ];

  let values = $state<Record<string, string>>(
    Object.fromEntries(samples.map((sample) => [sample.field.key, sample.setting.value ?? ''])),
  );

  function onReset(item: SettingsSchemaField): void {
    values[item.key] = item.default ?? '';
  }
</script>

<Section id="g-settings" data-gallery="settings" title="Settings">
  <div class="settings-form">
    {#each samples as sample (sample.field.key)}
      <SettingControl
        field={sample.field}
        setting={sample.setting}
        bind:value={values[sample.field.key]}
        error={sample.error}
        elsewhere={sample.elsewhere}
        view={sample.view}
        onreset={onReset}
      />
    {/each}
  </div>
</Section>
