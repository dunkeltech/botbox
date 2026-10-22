import { describe, expect, it } from 'vitest';
import type { components } from '@/types/openapi.gen';
import {
  activeMention,
  filterByQuery,
  groupSettingsBody,
  insertMention,
  mentionParts,
} from '@/components/team/text';

describe('team text helpers', () => {
  it('ignores an @ that continues a word and a finished mention', () => {
    expect(activeMention('mail@sam.com', 12)).toBeNull();
    expect(activeMention('@Sam notes', 10)).toBeNull();
    expect(activeMention('hi @Sa', 6)).toEqual({ start: 3, query: 'Sa' });
  });

  it('inserts one mention and keeps the text after the caret', () => {
    expect(insertMention('hi @Sa there', 3, 6, 'Sam')).toEqual({
      value: 'hi @Sam  there',
      caret: 8,
    });
  });

  it('filters names and marks only roster mentions', () => {
    expect(filterByQuery([{ name: 'Atlas' }, { name: 'Quiet' }], 'at')).toEqual(
      [{ name: 'Atlas' }],
    );
    expect(filterByQuery([{ name: 'Atlas' }], '   ')).toEqual([
      { name: 'Atlas' },
    ]);
    const parts = mentionParts('Ask @Sam and email@sam.com', ['Sam']);
    expect(
      parts.filter((part) => part.mention).map((part) => part.text),
    ).toEqual(['@Sam']);
  });

  it('keeps ask-before rules and members when one setting changes', () => {
    const group: components['schemas']['Group'] = {
      id: 'group-1',
      name: 'Launch',
      goal: 'Ship it',
      leadBotId: 'bot-1',
      botTalk: false,
      turnLimit: 20,
      answerPolicy: 'mentioned_else_lead',
      askBefore: [{ kind: 'custom', label: 'ship the release' }],
      budgetLimitMicros: 100,
      paused: false,
      pauseReason: '',
      turnsSinceHuman: 2,
      unread: 1,
      members: [
        { type: 'bot', id: 'bot-1', roleText: 'lead', name: 'Atlas' },
        { type: 'user', id: 'user-1', roleText: '', name: 'Sam' },
      ],
    };
    const body = groupSettingsBody(group, { botTalk: true });
    expect(body.botTalk).toBe(true);
    expect(body.askBefore).toEqual(group.askBefore);
    expect(body.members).toEqual([
      { type: 'bot', id: 'bot-1', roleText: 'lead' },
      { type: 'user', id: 'user-1', roleText: '' },
    ]);
    expect(body.budgetLimitMicros).toBe(100);
    expect(body.turnLimit).toBe(20);
  });
});
