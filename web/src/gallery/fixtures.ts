export interface MailRow {
  id: string;
  sender: string;
  subject: string;
  received: string;
  size: number;
}

export const mailRows: readonly MailRow[] = [
  { id: 'm1', sender: 'Hillfield Library', subject: 'Your hold is ready', received: '2026-11-14 09:12', size: 12 },
  { id: 'm2', sender: 'Cedar & Pine Supply', subject: 'Order 4471 shipped', received: '2026-11-13 17:40', size: 48 },
  { id: 'm3', sender: 'Book club', subject: 'December pick', received: '2026-11-13 08:05', size: 6 },
  { id: 'm4', sender: 'Riverside Dental', subject: 'Appointment reminder', received: '2026-11-12 14:22', size: 9 },
  { id: 'm5', sender: 'Allotment society', subject: 'Winter work day', received: '2026-11-10 19:58', size: 21 },
];

export const threads = [
  { id: 't1', title: 'Plan the garden shed order', meta: 'Today 09:40', unread: true },
  { id: 't2', title: 'Book club reminder', meta: 'Yesterday', unread: false },
  { id: 't3', title: 'Compare winter tyre quotes', meta: 'Mon', unread: false },
] as const;

export const effortOptions = [
  { value: 'low', label: 'Low' },
  { value: 'medium', label: 'Medium' },
  { value: 'high', label: 'High' },
] as const;

export const schemeOptions = [
  { value: 'auto', label: 'Auto' },
  { value: 'light', label: 'Light' },
  { value: 'dark', label: 'Dark' },
] as const;

export const deliveryOptions = [
  { value: 'now', label: 'Send now' },
  { value: 'digest', label: 'Add to the morning digest', help: 'Sent at 07:30' },
  { value: 'hold', label: 'Hold for review' },
] as const;
