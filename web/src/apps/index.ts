import { app as calendar } from './calendar/app';
import { app as library } from './library/app';
import { app as mail } from './mail/app';
import { app as packages } from './packages/app';
import { app as serverOps } from './server-ops/app';
import { app as torrents } from './torrents/app';
import type { AppDefinition } from './types';

export type { AppDefinition } from './types';

export const apps: readonly AppDefinition[] = [mail, calendar, packages, torrents, library, serverOps];
