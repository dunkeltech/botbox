// SPDX-License-Identifier: AGPL-3.0-only

import { expect, it } from 'vitest';
import { activeBotTurns, failedBotTurns } from './activity';

it('shows independent bots once and prefers running work over queued follow-ups', () => {
  const base = { queueAhead: 0, errorCode: '' };
  const selected = activeBotTurns([
    { ...base, id: 'new-yoda', botId: 'yoda', status: 'queued' },
    { ...base, id: 'running-yoda', botId: 'yoda', status: 'running' },
    { ...base, id: 'nova', botId: 'nova', status: 'running' },
    { ...base, id: 'unidentified', status: 'running' },
    { ...base, id: 'finished', botId: 'atlas', status: 'completed' },
    { ...base, id: 'failed', botId: 'other', status: 'failed' },
  ]);
  expect(selected.map((turn) => turn.id)).toEqual(['running-yoda', 'nova']);
  expect(activeBotTurns([])).toEqual([]);
});

it('reports only the latest failure for each bot and ignores superseded failures', () => {
  const base = { queueAhead: 0, errorCode: 'engine.provider_failed' };
  expect(
    failedBotTurns([
      { ...base, id: 'success', botId: 'yoda', status: 'completed' },
      { ...base, id: 'failed', botId: 'nova', status: 'failed' },
      { ...base, id: 'retry', botId: 'atlas', status: 'queued' },
      { ...base, id: 'old-yoda', botId: 'yoda', status: 'failed' },
      { ...base, id: 'old-atlas', botId: 'atlas', status: 'failed' },
      { ...base, id: 'unknown-bot', status: 'failed' },
    ]).map((turn) => turn.id),
  ).toEqual(['failed']);
});
