import { HttpResponse, http } from 'msw';
import type { Identity } from '../contract';
import { API_BASE } from '../contract';
import { simulatorHandlers } from './simulator';

const identity: Identity = { subject: 'operator', email: 'operator@example.test', name: 'Operator', api_version: 1 };

export const handlers = [http.get(`*${API_BASE}/me`, () => HttpResponse.json(identity)), ...simulatorHandlers];
