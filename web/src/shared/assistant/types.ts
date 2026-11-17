export interface PreviewMessage {
  id: number;
  subject: string;
  from_name: string;
  from_addr: string;
  date_ts: number;
}

export interface PreviewCount {
  label: string;
  count: number;
}

export interface PreviewGroup {
  name: string;
  count: number;
  senders: PreviewCount[];
  terms: string[];
  subjects: string[];
  messages: PreviewMessage[];
}

export interface PreviewCondition {
  field: string;
  op: string;
  value: unknown;
}

export interface PreviewCalendarEvent {
  kind: 'calendar_event';
  calendar: string;
  title: string;
  start: string;
  end: string;
  all_day: boolean;
  location: string;
  notes: string;
  source: unknown;
}

export interface PreviewCalendarReply {
  kind: 'calendar_rsvp';
  calendar: string;
  uid: string;
  response: string;
}

export interface PreviewCalendarUpdate {
  kind: 'calendar_update';
  calendar: string;
  uid: string;
  title: string;
  start: string;
  end: string;
  location: string;
}

export interface PreviewDeletionRule {
  kind: 'deletion_rule';
  reason: string;
  rule: { account: string; when: PreviewCondition[] };
  backlog: number;
  subjects: string[];
  senders: PreviewCount[];
  messages: PreviewMessage[];
}

export interface PreviewLabelMerge {
  kind: 'gmail_label_merge';
  from: string;
  into: string;
  from_count: number;
  into_count: number;
  messages: PreviewMessage[];
}

export interface PreviewInboxBacklog {
  kind: 'inbox_backlog';
  folder: string;
  groups: PreviewGroup[];
}

export interface PreviewTriageItem {
  id: number;
  subject: string;
  from_addr: string;
  date_ts: number;
  action: string;
  label: string;
}

export interface PreviewMailTriage {
  kind: 'mail_triage';
  count: number;
  messages: PreviewTriageItem[];
}

export interface PreviewFolderMerge {
  kind: 'rebalance_merge';
  folder: string;
  sources: { folder: string; count: number }[];
  into: string;
  group: PreviewGroup;
}

export interface PreviewFolderSplit {
  kind: 'rebalance_split';
  folder: string;
  groups: PreviewGroup[];
}

export interface PreviewReminder {
  kind: 'reminder';
  commitment_id: number;
  text: string;
  due: string;
  owner: string;
  message_id: number;
  tz: string;
  remind_at: string;
  subject: string;
}

export interface PreviewToolCall {
  kind: 'tool_call';
  tool: string;
  args: unknown;
}

export type ProposalChange =
  | PreviewCalendarEvent
  | PreviewCalendarReply
  | PreviewCalendarUpdate
  | PreviewDeletionRule
  | PreviewLabelMerge
  | PreviewInboxBacklog
  | PreviewMailTriage
  | PreviewFolderMerge
  | PreviewFolderSplit
  | PreviewReminder
  | PreviewToolCall;

export interface MailDraftChange {
  kind: 'mail_draft';
  account: string;
  to: string;
  cc: string;
  subject: string;
  body: string;
  unexpected_recipients: string[];
}

export type ApprovalChange = ProposalChange | MailDraftChange;

export interface Arg {
  label: string;
  value: string;
}

export interface PlainPreview {
  label: string;
  args: Arg[];
  preview: unknown;
}

export interface CalendarSlot {
  title: string;
  start: string;
  end: string;
  location: string;
}

export interface Consequence {
  reversible: string;
  audience: string;
  cost: string;
  outside: string;
}

export interface Approval {
  id: string;
  kind: string;
  title: string;
  from: string;
  fromHref?: string;
  created: string;
  why: string;
  change: ApprovalChange | null;
  plain: PlainPreview;
  current?: CalendarSlot;
  consequence: Consequence;
  primary: string;
  irreversible: boolean;
  done: string;
  sending?: string;
}

export interface DecidedElsewhere {
  outcome?: string;
  where?: string;
  at: string;
}

export interface Outcome {
  value: string;
  label: string;
  ask: string;
  effect: string;
  done: string;
}

export type Blocked = { reason: string; repair: string } | { reason: string; outcomes: readonly Outcome[] };

export type Decision = 'approve' | 'decline' | 'later';

export interface WebResult {
  title: string;
  url: string;
}

export type ToolState = 'running' | 'done' | 'failed' | 'waiting';

export interface ToolCall {
  id: string;
  name: string;
  args: string;
  state: ToolState;
  progress?: string;
  preview?: string;
  results?: WebResult[];
}

export interface DigestLine {
  text: string;
  url?: string;
}

export type { HomeWeather } from '../../contract';
