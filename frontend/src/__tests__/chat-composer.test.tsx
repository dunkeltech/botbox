import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from '@testing-library/react';
import { NextIntlClientProvider } from 'next-intl';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { ChatPanel } from '@/components/chat/ChatPanel';
import { MarkdownMessage } from '@/components/chat/MarkdownMessage';
import { GroupConversation } from '@/components/team/GroupConversation';
import { requestJson } from '@/lib/api-client';
import type { components } from '@/types/openapi.gen';
import messages from '../../messages/en.json';

vi.mock('@/components/chat/MarkdownMessage', async (importOriginal) => {
  const actual =
    await importOriginal<typeof import('@/components/chat/MarkdownMessage')>();
  return { ...actual, MarkdownMessage: vi.fn(actual.MarkdownMessage) };
});
vi.mock('@/lib/api-client', () => ({
  getAccessToken: vi.fn(() => null),
  requestJson: vi.fn(),
}));

const request = vi.mocked(requestJson);
const renderMessage = vi.mocked(MarkdownMessage);
type ChatMessage = components['schemas']['ChatMessage'];
type ChatHistory = components['schemas']['ChatHistory'];
type ChatKind = 'group' | 'direct';

const group: components['schemas']['Group'] = {
  id: 'group-1',
  name: 'Launch',
  goal: '',
  leadBotId: 'bot-1',
  botTalk: false,
  turnLimit: 20,
  answerPolicy: 'mentioned_else_lead',
  askBefore: [],
  paused: false,
  pauseReason: '',
  turnsSinceHuman: 0,
  unread: 0,
  members: [{ id: 'bot-1', type: 'bot', name: 'Atlas', roleText: '' }],
};
const clients: QueryClient[] = [];

afterEach(() => {
  cleanup();
  for (const client of clients.splice(0)) client.clear();
  vi.clearAllMocks();
});

function message(position: number): ChatMessage {
  return {
    id: `message-${position}`,
    position,
    senderType: 'bot',
    senderBotId: 'bot-1',
    kind: 'text',
    content: `**Reply ${position}**`,
    createdAt: '2026-11-17T12:00:00Z',
  };
}

function show(kind: ChatKind, count: number) {
  const isGroup = kind === 'group';
  const send = vi.fn<() => Promise<unknown>>().mockResolvedValue(undefined);
  const history = {
    messages: Array.from({ length: count }, (_, i) => message(i + 1)),
    turns: [],
  };
  const historyKey = isGroup
    ? ['group-messages', 'ws-1', group.id]
    : ['chat', 'ws-1', 'user-1', 'conversation-1'];
  const path = isGroup
    ? `/api/workspaces/ws-1/groups/${group.id}/messages`
    : '/api/workspaces/ws-1/conversations/conversation-1/messages';
  request.mockImplementation((url, options) => {
    if (options?.method === 'POST' && url === path) return send();
    if (url.endsWith('/dm')) return Promise.resolve({ id: 'conversation-1' });
    if (url.endsWith('/approvals')) return Promise.resolve({ approvals: [] });
    if (url.endsWith('/budgets?botId=bot-1'))
      return Promise.resolve({ budgets: [] });
    if (url === path)
      return Promise.resolve({
        ...history,
        messages: history.messages.slice(-50),
      });
    const before = Number(
      new URL(url, 'http://localhost').searchParams.get('before'),
    );
    if (url.startsWith(`${path}?before=`)) {
      return Promise.resolve({
        ...history,
        messages: history.messages.slice(Math.max(0, before - 51), before - 1),
      });
    }
    throw new Error(`Unexpected request ${url}`);
  });
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  clients.push(client);
  render(
    <NextIntlClientProvider locale="en" timeZone="UTC" messages={messages}>
      <QueryClientProvider client={client}>
        {isGroup ? (
          <GroupConversation
            workspaceId="ws-1"
            userId="user-1"
            group={group}
            role="member"
            canPost
            query=""
          />
        ) : (
          <ChatPanel
            workspaceId="ws-1"
            userId="user-1"
            botId="bot-1"
            name="Atlas"
            avatarSeed="atlas"
          />
        )}
      </QueryClientProvider>
    </NextIntlClientProvider>,
  );
  return { client, historyKey, input: screen.getByRole('textbox'), path, send };
}

describe('chat composer', () => {
  it.each([
    ['group', 50],
    ['direct', 150],
  ] as const)(
    'keeps the %s transcript out of draft updates with %i loaded messages',
    async (kind, count) => {
      const { client, historyKey, input } = show(kind, count);
      await screen.findByText(`Reply ${count}`);
      for (let remaining = count - 50; remaining > 0; remaining -= 50) {
        fireEvent.click(
          screen.getByRole('button', { name: messages.chat.older }),
        );
        await screen.findByText(`Reply ${Math.max(1, remaining - 49)}`);
      }
      expect(screen.getAllByRole('article')).toHaveLength(count);
      expect(renderMessage).toHaveBeenCalled();
      renderMessage.mockClear();

      const requestCount = request.mock.calls.length;
      for (const value of ['H', 'Hi', 'Hi ']) {
        fireEvent.change(input, {
          target: { value, selectionStart: value.length },
        });
      }
      let draft = 'Hi ';
      if (kind === 'group') {
        fireEvent.change(input, {
          target: { value: 'Hi @At', selectionStart: 6 },
        });
        expect(
          screen.getByRole('option', { name: /Atlas/ }),
        ).toBeInTheDocument();
        fireEvent.keyDown(input, { key: 'Enter' });
        draft = 'Hi @Atlas ';
      }
      expect(input).toHaveValue(draft);
      expect(request).toHaveBeenCalledTimes(requestCount);
      expect(renderMessage.mock.calls.length).toBe(0);

      act(() => {
        client.setQueryData<ChatHistory>(historyKey, (history) => ({
          messages: [...(history?.messages ?? []), message(count + 1)],
          turns: [],
        }));
      });
      expect(await screen.findByText(`Reply ${count + 1}`)).toBeInTheDocument();
      expect(input).toHaveValue(draft);
    },
  );

  it.each(['group', 'direct'] as const)(
    'keeps a failed %s draft and sends it with Enter on retry',
    async (kind) => {
      const { input, path, send } = show(kind, 1);
      await screen.findByText('Reply 1');
      const pending = Promise.withResolvers<void>();
      send.mockReturnValueOnce(pending.promise);
      fireEvent.change(input, { target: { value: '   ' } });
      fireEvent.keyDown(input, { key: 'Enter' });
      fireEvent.change(input, { target: { value: 'First line' } });
      expect(fireEvent.keyDown(input, { key: 'Enter', shiftKey: true })).toBe(
        true,
      );
      expect(send).not.toHaveBeenCalled();

      const content = 'First line\nSecond line';
      fireEvent.change(input, { target: { value: content } });
      fireEvent.keyDown(input, { key: 'Enter' });
      expect(screen.getByRole('button', { name: /sending/i })).toBeDisabled();
      fireEvent.submit(input.closest('form')!);
      expect(send).toHaveBeenCalledTimes(1);
      await act(() => {
        pending.reject(new Error('private details'));
        return pending.promise.catch(() => undefined);
      });
      expect(
        await screen.findByText(
          kind === 'group' ? messages.team.sendFailed : messages.chat.error,
        ),
      ).toBeInTheDocument();
      expect(screen.queryByText('private details')).toBeNull();
      expect(input).toHaveValue(content);

      fireEvent.keyDown(input, { key: 'Enter' });
      await waitFor(() => expect(input).toHaveValue(''));
      expect(send).toHaveBeenCalledTimes(2);
      expect(request).toHaveBeenCalledWith(path, {
        method: 'POST',
        body: { id: expect.any(String) as string, content },
      });
      expect(
        screen.queryByText(
          kind === 'group' ? messages.team.sendFailed : messages.chat.error,
        ),
      ).toBeNull();
    },
  );
});
