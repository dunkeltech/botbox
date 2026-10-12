import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from '@testing-library/react';
import { NextIntlClientProvider } from 'next-intl';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ChatPanel } from '@/components/chat/ChatPanel';
import { BudgetPanel } from '@/components/chat/BudgetPanel';
import { QueryProvider } from '@/components/providers/QueryProvider';
import { getAccessToken, requestJson } from '@/lib/api-client';
import messages from '../../messages/en.json';

vi.mock('@/lib/api-client', () => ({
  requestJson: vi.fn(),
  getAccessToken: vi.fn(() => null),
}));
const request = vi.mocked(requestJson);
function show(child: React.ReactNode) {
  return render(
    <NextIntlClientProvider locale="en" messages={messages}>
      <QueryProvider>{child}</QueryProvider>
    </NextIntlClientProvider>,
  );
}
beforeEach(() => {
  vi.mocked(getAccessToken).mockReturnValue(null);
  request.mockImplementation((path: string) => {
    if (path.endsWith('/dm'))
      return Promise.resolve({ id: 'conversation-1', botId: 'bot-1' });
    if (path.includes('/messages'))
      return Promise.resolve({
        messages: [
          {
            id: 'message-1',
            position: 1,
            senderType: 'bot',
            kind: 'text',
            content: '<script>unsafe()</script>',
            createdAt: '2026-11-15T10:00:00Z',
          },
        ],
        turns: [
          { id: 'turn-1', status: 'queued', queueAhead: 2, errorCode: '' },
        ],
      });
    return Promise.resolve({
      spendTodayMicros: 800000,
      budgets: [
        {
          id: 'budget-1',
          period: 'day',
          limitMicros: 1000000,
          spentMicros: 800000,
          warning: true,
          exceeded: false,
        },
      ],
    });
  });
});
afterEach(() => {
  cleanup();
  vi.clearAllMocks();
  vi.unstubAllGlobals();
});
describe('Phase 4 chat and budgets', () => {
  it('shows private history, queue position, budget warning and sends a message', async () => {
    const { container } = show(
      <ChatPanel
        workspaceId="ws-1"
        userId="user-1"
        botId="bot-1"
        name="Atlas"
        avatarSeed="atlas-seed"
      />,
    );
    expect(
      await screen.findByText('Queued · 2 turns ahead'),
    ).toBeInTheDocument();
    expect(screen.getByText('<script>unsafe()</script>')).toBeInTheDocument();
    expect(container.querySelector('script')).toBeNull();
    expect(screen.getByText(messages.chat.privacy)).toBeInTheDocument();
    expect(
      await screen.findByText(messages.chat.budgetWarning),
    ).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText('Message'), {
      target: { value: 'Run tests' },
    });
    fireEvent.submit(
      screen.getByRole('button', { name: 'Send' }).closest('form')!,
    );
    await waitFor(() => {
      const sent = request.mock.calls.find(
        ([path, options]) =>
          path ===
            '/api/workspaces/ws-1/conversations/conversation-1/messages' &&
          options?.method === 'POST',
      );
      expect(sent).toBeDefined();
      const body = sent?.[1]?.body as { id: string; content: string };
      expect(body.content).toBe('Run tests');
      expect(body.id).toMatch(/^[0-9a-f-]{36}$/);
    });
  });
  it('does not reuse another user’s private history after switching accounts', async () => {
    const frame = (userId: string) => (
      <NextIntlClientProvider locale="en" messages={messages}>
        <QueryProvider>
          <ChatPanel
            key={userId}
            userId={userId}
            workspaceId="ws-1"
            botId="bot-1"
            name="Atlas"
            avatarSeed="atlas-seed"
          />
        </QueryProvider>
      </NextIntlClientProvider>
    );
    const view = render(frame('user-1'));
    await screen.findByText('<script>unsafe()</script>');
    request.mockImplementation(() => new Promise(() => {}));
    view.rerender(frame('user-2'));
    expect(screen.queryByText('<script>unsafe()</script>')).toBeNull();
  });
  it('prevents viewers from creating or sending a DM', () => {
    show(
      <ChatPanel
        workspaceId="ws-1"
        userId="user-1"
        botId="bot-1"
        name="Atlas"
        avatarSeed="atlas-seed"
        canChat={false}
      />,
    );
    expect(screen.getByRole('button', { name: 'Send' })).toBeDisabled();
    expect(screen.getByLabelText('Message')).toBeDisabled();
    expect(request.mock.calls.some(([path]) => path.endsWith('/dm'))).toBe(
      false,
    );
  });
  it('reports dependency failures without showing raw errors', async () => {
    request.mockRejectedValue(new Error('private dependency detail'));
    show(
      <ChatPanel
        workspaceId="ws-1"
        userId="user-1"
        botId="bot-1"
        name="Atlas"
        avatarSeed="atlas-seed"
      />,
    );
    expect(await screen.findByText(messages.chat.error)).toBeInTheDocument();
    expect(screen.queryByText('private dependency detail')).toBeNull();
  });
  it('restricts budget editing to administrators', async () => {
    const view = show(<BudgetPanel workspaceId="ws-1" />);
    expect(await screen.findByText(/Spend today:/)).toBeInTheDocument();
    expect(screen.getByRole('progressbar')).toHaveAttribute(
      'aria-valuenow',
      '80',
    );
    expect(screen.getByText('80% used')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Save budget' })).toBeNull();
    view.unmount();
    show(<BudgetPanel workspaceId="ws-1" botId="bot-1" canManage />);
    fireEvent.change(screen.getByLabelText('Limit (USD)'), {
      target: { value: '2' },
    });
    fireEvent.submit(
      screen.getByRole('button', { name: 'Save budget' }).closest('form')!,
    );
    await waitFor(() =>
      expect(request).toHaveBeenCalledWith(
        '/api/workspaces/ws-1/budgets',
        expect.objectContaining({
          method: 'PUT',
          body: { botId: 'bot-1', period: 'day', limitMicros: 2000000 },
        }),
      ),
    );
  });
});

describe('live runner messages', () => {
  it('shows complete CLI messages and retains text during tool activity', async () => {
    class FakeSocket {
      static current: FakeSocket;
      onmessage: ((event: MessageEvent<string>) => void) | null = null;
      close = vi.fn();
      constructor() {
        FakeSocket.current = this;
      }
      emit(data: unknown) {
        this.onmessage?.({
          data: JSON.stringify(data),
        } as MessageEvent<string>);
      }
    }
    vi.stubGlobal('WebSocket', FakeSocket);
    vi.mocked(getAccessToken).mockReturnValue('fixture-token');
    show(
      <ChatPanel
        workspaceId="ws-1"
        userId="user-1"
        botId="bot-1"
        name="Atlas"
        avatarSeed="atlas-seed"
      />,
    );
    await waitFor(() =>
      expect(FakeSocket.current?.onmessage).toBeTypeOf('function'),
    );
    act(() =>
      FakeSocket.current.emit({ Kind: 'message', Text: 'Claude reply' }),
    );
    expect(screen.getByText('Claude reply')).toBeInTheDocument();
    act(() => FakeSocket.current.emit({ Kind: 'status', Status: 'thinking' }));
    expect(screen.getByText('Claude reply')).toBeInTheDocument();
    act(() => {
      FakeSocket.current.emit({ Kind: 'text_delta', Text: ' live delta' });
      FakeSocket.current.emit({ Kind: 'message', Text: ' live delta' });
    });
    expect(screen.getByText('Claude reply live delta')).toBeInTheDocument();
    act(() => FakeSocket.current.emit({ Kind: 'status', Status: 'idle' }));
    expect(
      screen.queryByText('Claude reply live delta'),
    ).not.toBeInTheDocument();
  });
});

describe('turn failure recovery', () => {
  it.each([
    ['engine.cli_unavailable', 'isolated Docker computer'],
    ['engine.computer_not_running', 'Start the bot computer'],
    ['engine.computer_restart_required', 'replace its simulated computer'],
    ['engine.proxy_unavailable', 'egress proxy'],
    ['engine.credentials_missing', 'provider credential'],
    ['fixture-private-key', 'could not finish this turn'],
    ['__proto__', 'could not finish this turn'],
  ])('shows a safe recovery hint for %s', async (code, expected) => {
    request.mockImplementation((path: string) => {
      if (path.endsWith('/dm'))
        return Promise.resolve({ id: 'conv', botId: 'bot' });
      if (path.includes('/messages'))
        return Promise.resolve({
          messages: [],
          turns: [
            { id: 'turn', status: 'failed', queueAhead: 0, errorCode: code },
          ],
        });
      return Promise.resolve({ budgets: [], spendTodayMicros: 0 });
    });
    show(
      <ChatPanel
        workspaceId="ws"
        userId="user"
        botId="bot"
        name="Atlas"
        avatarSeed="atlas-seed"
      />,
    );
    expect(await screen.findByRole('alert')).toHaveTextContent(expected);
    expect(screen.queryByText('fixture-private-key')).not.toBeInTheDocument();
  });
});
