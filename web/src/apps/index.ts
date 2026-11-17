export interface AppScreen {
  key: string;
  label: string;
  path: string;
  wide?: boolean;
}

export const screens: Readonly<Record<string, string>> = {};

export const appScreens: Readonly<Record<string, readonly AppScreen[]>> = {
  calendar: [
    { key: 'agenda', label: 'Agenda', path: '/apps/calendar' },
    { key: 'week', label: 'Week', path: '/apps/calendar/week', wide: true },
    { key: 'candidates', label: 'Candidates', path: '/apps/calendar/candidates' },
  ],
};
