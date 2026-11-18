import type {
  Approval,
  ApprovalChange,
  Consequence,
  DigestLine,
  HomeWeather,
  PlainPreview,
} from '../shared/assistant/types';

const letter = [
  'Hi Ilse,',
  '',
  'Thank you for hosting the book club last month. Everyone is still talking about the pear cake.',
  '',
  'For December we picked "The Salt Orchard". The plan for the evening:',
  '',
  '1. We meet at the Hillfield Library reading room at 19:00 on Thursday 10 December.',
  '2. Pieter brings tea and the big thermos.',
  '3. Marta leads the discussion and has printed the question sheet.',
  '4. Everyone brings one small snack to share.',
  '',
  'The library asks that we leave the room by 21:30, so we will start on time.',
  '',
  'Could you bring the spare copies you mentioned? Two people still need one. If that is a bother, the library has one copy we can borrow.',
  '',
  'Parking on Mill Lane is free after 18:00. The side entrance is open until 19:15. After that, ring the bell and someone will let you in.',
  '',
  'If the weather turns, we move to the cafe across the road. I will send a note before 16:00 that day.',
  '',
  'Some of us are walking over together from the station at 18:40. You are welcome to join.',
  '',
  'Looking ahead: January is the poetry month. Please send me one or two short poems you would like to read aloud by 20 December.',
  '',
  'Thanks again for everything,',
  'Sam',
].join('\n');

const reversible: Consequence = {
  reversible: 'Yes, for 10 seconds with Undo, then from History',
  audience: 'Only you',
  cost: 'Free',
  outside: 'Nothing leaves atrium',
};

function plain(label: string, preview: unknown = null): PlainPreview {
  return { label, args: [], preview };
}

export const draftApproval: Approval = {
  id: 'a-draft',
  kind: 'mail_draft',
  title: 'Send the December reminder to Ilse',
  from: 'Book club reminder',
  fromHref: '#g-approvals',
  created: '2026-11-19T09:02:00+01:00',
  why: 'You asked atrium to draft the reminder after the last meeting. Mail always waits for you before it leaves.',
  change: {
    kind: 'mail_draft',
    account: 'sam@example.test',
    to: 'Ilse de Wit <ilse@example.test>',
    cc: '',
    subject: 'Book club in December',
    body: letter,
    unexpected_recipients: [],
  },
  plain: plain('Send email'),
  consequence: {
    reversible: 'No. Mail cannot be recalled once sent',
    audience: 'Ilse de Wit',
    cost: 'Free',
    outside: 'Sends one email from sam@example.test',
  },
  primary: 'Send email to Ilse',
  irreversible: true,
  done: 'Sent to Ilse de Wit',
  sending: 'Sending to Ilse de Wit',
};

export const moveApproval: Approval = {
  id: 'a-move',
  kind: 'calendar_update',
  title: 'Move the dentist visit to Friday',
  from: 'Riverside Dental mail',
  created: '2026-11-19T08:15:00+01:00',
  why: 'Riverside Dental asked to move the visit. Friday morning is free in your calendar.',
  change: {
    kind: 'calendar_update',
    calendar: 'Home',
    uid: 'evt-2291',
    title: 'Dentist check-up',
    start: '2026-11-27T09:30:00+01:00',
    end: '2026-11-27T10:00:00+01:00',
    location: 'Riverside Dental, Mill Lane 4',
  },
  current: {
    title: 'Dentist check-up',
    start: '2026-11-25T15:00:00+01:00',
    end: '2026-11-25T15:30:00+01:00',
    location: 'Riverside Dental, Mill Lane 4',
  },
  plain: plain('Update event'),
  consequence: { ...reversible, audience: 'Anyone who shares the Home calendar' },
  primary: 'Move the event',
  irreversible: false,
  done: 'Moved to Friday 09:30',
};

export const triageApproval: Approval = {
  id: 'a-triage',
  kind: 'mail_triage',
  title: 'File 3 shipping notices',
  from: 'Inbox sweep',
  created: '2026-11-19T07:30:00+01:00',
  why: 'These match your rule for delivered orders.',
  change: {
    kind: 'mail_triage',
    count: 3,
    messages: [
      {
        id: 11,
        subject: 'Order 4471 shipped',
        from_addr: 'orders@cedar-pine.example',
        date_ts: 1795075200,
        action: 'Move',
        label: 'Orders',
      },
      {
        id: 12,
        subject: 'Your parcel is on its way',
        from_addr: 'track@parcel.example',
        date_ts: 1795161600,
        action: 'Move',
        label: 'Orders',
      },
      {
        id: 13,
        subject: 'Delivered: brass screws',
        from_addr: 'orders@cedar-pine.example',
        date_ts: 1795248000,
        action: 'Archive',
        label: 'Archive',
      },
    ],
  },
  plain: plain('Triage mail'),
  consequence: reversible,
  primary: 'File 3 messages',
  irreversible: false,
  done: 'Filed 3 messages',
};

export const unknownApproval: Approval = {
  id: 'a-unknown',
  kind: 'garden_watering',
  title: 'Start the allotment watering timer',
  from: 'Allotment helper',
  created: '2026-11-19T06:45:00+01:00',
  why: 'The forecast shows no rain for four days.',
  change: null,
  plain: {
    label: 'Start watering',
    args: [
      { label: 'Plot', value: 'Plot 14, Allotment society' },
      { label: 'Duration', value: '20 minutes' },
    ],
    preview: 'Turns on the drip line at 06:00 tomorrow.',
  },
  consequence: { ...reversible, outside: 'Sends one command to the plot timer' },
  primary: 'Start the timer',
  irreversible: false,
  done: 'Timer set for 06:00',
};

export const elsewhereApproval: Approval = {
  id: 'a-elsewhere',
  kind: 'calendar_rsvp',
  title: 'Accept the winter work day',
  from: 'Allotment society',
  created: '2026-11-18T19:58:00+01:00',
  why: 'You go to every work day.',
  change: { kind: 'calendar_rsvp', calendar: 'Home', uid: 'evt-3310', response: 'accepted' },
  current: {
    title: 'Winter work day',
    start: '2026-12-05T10:00:00+01:00',
    end: '2026-12-05T14:00:00+01:00',
    location: 'Allotment gate',
  },
  plain: plain('Reply to invite'),
  consequence: { ...reversible, audience: 'The organizer, Marta' },
  primary: 'Accept invite',
  irreversible: false,
  done: 'Accepted',
};

export const blockedApproval: Approval = {
  id: 'a-blocked',
  kind: 'reminder',
  title: 'Remind you to return the library books',
  from: 'Hillfield Library mail',
  created: '2026-11-18T12:00:00+01:00',
  why: 'The loan ends on 2 December.',
  change: {
    kind: 'reminder',
    commitment_id: 41,
    text: 'Return the two library books',
    due: '2026-12-02',
    owner: 'You',
    message_id: 77,
    tz: 'Europe/Amsterdam',
    remind_at: '2026-12-01T18:00:00+01:00',
    subject: 'Your loans are due soon',
  },
  plain: plain('Set reminder'),
  consequence: reversible,
  primary: 'Set reminder',
  irreversible: false,
  done: 'Reminder set',
};

export const previews: readonly { label: string; change: ApprovalChange | null; plain: PlainPreview }[] = [
  {
    label: 'New event',
    plain: plain('Add event'),
    change: {
      kind: 'calendar_event',
      calendar: 'Home',
      title: 'Seed swap at the library',
      start: '2026-12-12T13:00:00+01:00',
      end: '2026-12-12T15:00:00+01:00',
      all_day: false,
      location: 'Hillfield Library',
      notes: 'Bring the bean seeds in the blue tin.',
      source: null,
    },
  },
  {
    label: 'Deletion rule',
    plain: plain('Add rule'),
    change: {
      kind: 'deletion_rule',
      reason: 'You never open these.',
      rule: { account: 'sam@example.test', when: [{ field: 'from', op: 'ends_with', value: '@deals.example' }] },
      backlog: 214,
      subjects: ['Weekend offers'],
      senders: [{ label: 'Deals Weekly', count: 214 }],
      messages: [
        {
          id: 21,
          subject: 'Weekend offers',
          from_name: 'Deals Weekly',
          from_addr: 'news@deals.example',
          date_ts: 1795075200,
        },
      ],
    },
  },
  {
    label: 'Label merge',
    plain: plain('Merge labels'),
    change: {
      kind: 'gmail_label_merge',
      from: 'Receipts 2025',
      into: 'Receipts',
      from_count: 38,
      into_count: 412,
      messages: [
        {
          id: 31,
          subject: 'Receipt for order 3902',
          from_name: 'Cedar & Pine Supply',
          from_addr: 'orders@cedar-pine.example',
          date_ts: 1795161600,
        },
      ],
    },
  },
  {
    label: 'Inbox backlog',
    plain: plain('Clear backlog'),
    change: {
      kind: 'inbox_backlog',
      folder: 'Inbox',
      groups: [
        {
          name: 'Newsletters',
          count: 61,
          senders: [{ label: 'Garden Weekly', count: 40 }],
          terms: [],
          subjects: [],
          messages: [],
        },
        {
          name: 'Shipping',
          count: 12,
          senders: [{ label: 'Parcel Post', count: 12 }],
          terms: [],
          subjects: [],
          messages: [],
        },
      ],
    },
  },
  {
    label: 'Folder merge',
    plain: plain('Merge folders'),
    change: {
      kind: 'rebalance_merge',
      folder: 'Archive',
      sources: [
        { folder: 'Club 2024', count: 18 },
        { folder: 'Club 2025', count: 27 },
      ],
      into: 'Book club',
      group: { name: 'Book club', count: 45, senders: [], terms: [], subjects: [], messages: [] },
    },
  },
  {
    label: 'Folder split',
    plain: plain('Split folder'),
    change: {
      kind: 'rebalance_split',
      folder: 'Household',
      groups: [
        {
          name: 'Utilities',
          count: 33,
          senders: [{ label: 'Hillfield Water', count: 20 }],
          terms: [],
          subjects: [],
          messages: [],
        },
        { name: 'Repairs', count: 9, senders: [], terms: [], subjects: [], messages: [] },
      ],
    },
  },
  {
    label: 'Tool call',
    plain: plain('Run tool'),
    change: { kind: 'tool_call', tool: 'notes.append', args: { note: 'Shed order', text: 'Twelve cedar boards' } },
  },
  {
    label: 'Unknown kind with data',
    plain: { label: 'Rotate the backup key', args: [], preview: { vault: 'home', keep_old_days: 7 } },
    change: null,
  },
];

export const digestLines: DigestLine[] = [
  { text: 'Ilse replied about the December book club', url: '/inbox' },
  { text: 'Dentist visit moved to Friday 09:30', url: '/apps/calendar' },
  { text: 'Cedar & Pine shipped order 4471' },
  { text: '3 records from marked sources', url: '/knowledge/review' },
  { text: 'Allotment work day is on 5 December', url: 'https://allotment.example/workday' },
  { text: 'Two new receipts filed under Receipts' },
  { text: 'Water bill is ready' },
];

export const weather: HomeWeather = {
  state: 'ok',
  fetched_at: 1_795_075_200,
  failure: null,
  location: 'Hillfield',
  timezone: 'Europe/Amsterdam',
  current: {
    time: '2026-11-19T09:00',
    temperature_c: 6.4,
    apparent_c: 3.1,
    humidity: 82,
    precipitation_mm: 0,
    weather_code: 3,
    condition: 'Overcast',
    wind_kmh: 18,
    wind_direction: 240,
    is_day: true,
  },
  hours: [],
  today: {
    date: '2026-11-19',
    high_c: 8.2,
    low_c: 2.7,
    precipitation_probability: 60,
    weather_code: 61,
    condition: 'Light rain',
    sunrise: '08:04',
    sunset: '16:42',
  },
};
