// SPDX-License-Identifier: AGPL-3.0-only

import type { components } from '@/types/openapi.gen';

type Turn = components['schemas']['ChatTurn'];
const priority = { running: 3, waiting_secret: 2, queued: 1 };

/** Show one activity per bot, preferring its running turn over queued follow-ups. */
export function activeBotTurns(turns: Turn[]): Turn[] {
  const selected = new Map<string, Turn>();
  for (const turn of turns) {
    if (!turn.botId || !Object.hasOwn(priority, turn.status)) continue;
    const current = selected.get(turn.botId);
    const rank = priority[turn.status as keyof typeof priority];
    if (!current || rank > priority[current.status as keyof typeof priority]) {
      selected.set(turn.botId, turn);
    }
  }
  return [...selected.values()];
}

/** Show only each bot's latest failed turn, hiding failures superseded by new work. */
export function failedBotTurns(turns: Turn[]): Turn[] {
  const latest = new Map<string, Turn>();
  for (const turn of turns) {
    if (turn.botId && !latest.has(turn.botId)) latest.set(turn.botId, turn);
  }
  return [...latest.values()].filter((turn) =>
    ['failed', 'interrupted', 'budget_exceeded'].includes(turn.status),
  );
}
