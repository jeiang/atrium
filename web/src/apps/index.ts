export interface AppScreen {
  key: string;
  label: string;
  path: string;
  wide?: boolean;
}

export const screens: Readonly<Record<string, string>> = {
  '/today': '/',
  '/review': '/inbox',
  '/activity': '/inbox',
  '/drafts': '/inbox',
  '/triage': '/apps/mail/decisions',
  '/rules': '/apps/mail/rules',
  '/rules/new': '/apps/mail/rules',
  '/live': '/apps/mail/mode',
  '/watches': '/apps/packages',
  '/loops': '/plan/waiting',
  '/projects/new': '/knowledge/spaces',
  '/library/files': '/knowledge/files',
  '/library/knowledge': '/knowledge',
  '/library/knowledge?state=unconfirmed': '/knowledge/review',
  '/library/knowledge/profile': '/knowledge/me',
  '/library/knowledge/new': '/knowledge',
  '/scheduled': '/assistant/routines',
  '/settings/mail': '/settings/mail',
  '/settings/auth': '/settings/sign-in',
  '/settings/assistant': '/assistant/behavior',
  '/settings/permissions': '/assistant/authority',
  '/settings/usage': '/assistant/spend',
  '/settings/export': '/settings/knowledge-export',
  '/settings/media': '/settings/library',
  '/settings/packages': '/settings/apps',
  '/settings/weather': '/settings/connections',
  '/settings/connections/google': '/settings/connections/google/default',
  '/apps/media/downloads': '/apps/torrents',
  '/apps/media/torrents': '/apps/torrents',
  '/apps/media/add': '/apps/torrents',
  '/apps/media/channels': '/apps/media/following',
  '/apps/media/library': '/apps/media',
};

export const movedScreens: readonly (readonly [RegExp, string])[] = [
  [/^\/rules\/([^/]+)\/edit$/, '/apps/mail/rules/$1'],
  [/^\/messages\/([^/]+)(?:\/correct)?$/, '/apps/mail/message/$1'],
  [/^\/chat\/([^/]+)\/(?:work|history|runs\/[^/]+)$/, '/chat/$1'],
  [/^\/projects\/([^/]+)\/(?:edit|files)$/, '/knowledge/spaces/$1'],
  [/^\/library\/knowledge\/([^/]+)$/, '/knowledge/$1'],
  [/^\/apps\/media\/torrents\/[^/]+\/files$/, '/apps/torrents'],
  [/^\/apps\/media\/library\/(?:movie|tv)\/[^/]+$/, '/apps/media'],
];

export const appScreens: Readonly<Record<string, readonly AppScreen[]>> = {
  mail: [
    { key: 'messages', label: 'Messages', path: '/apps/mail' },
    { key: 'decisions', label: 'Decisions', path: '/apps/mail/decisions' },
    { key: 'rules', label: 'Rules', path: '/apps/mail/rules' },
    { key: 'mode', label: 'Mode', path: '/apps/mail/mode' },
    { key: 'accounts', label: 'Accounts', path: '/apps/mail/accounts' },
  ],
  calendar: [
    { key: 'agenda', label: 'Agenda', path: '/apps/calendar' },
    { key: 'week', label: 'Week', path: '/apps/calendar/week', wide: true },
    { key: 'candidates', label: 'Candidates', path: '/apps/calendar/candidates' },
  ],
};
