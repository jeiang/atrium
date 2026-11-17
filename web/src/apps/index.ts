export interface AppScreen {
  key: string;
  label: string;
  path: string;
  wide?: boolean;
}

export const screens: Readonly<Record<string, string>> = {
  '/apps/media/downloads': '/apps/torrents',
  '/apps/media/library': '/apps/media',
  '/apps/media/channels': '/apps/media/following',
  '/watches': '/apps/packages',
};

export const appScreens: Readonly<Record<string, readonly AppScreen[]>> = {
  mail: [{ key: 'messages', label: 'Messages', path: '/apps/mail' }],
  calendar: [
    { key: 'agenda', label: 'Agenda', path: '/apps/calendar' },
    { key: 'week', label: 'Week', path: '/apps/calendar/week', wide: true },
    { key: 'candidates', label: 'Candidates', path: '/apps/calendar/candidates' },
  ],
};
