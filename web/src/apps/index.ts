export interface AppScreen {
  key: string;
  label: string;
  path: string;
  wide?: boolean;
}

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
