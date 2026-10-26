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
import { getAccessToken, requestBlob, requestJson } from '@/lib/api-client';
import messages from '../../messages/en.json';

vi.mock('@/lib/api-client', () => ({
  requestJson: vi.fn(),
  requestBlob: vi.fn(),
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

describe('tool lines in chat', () => {
  it('shows an action sentence and the screenshot, not the sandbox path', async () => {
    const path =
      '/home/bot/.claude/projects/-home-bot/fe505481-b639-48b3-95ab-6e1db702084f/tool-results/mcp-botbox-desktop-blob-1.png';
    request.mockImplementation((apiPath: string) => {
      if (apiPath.endsWith('/dm'))
        return Promise.resolve({ id: 'conversation-1', botId: 'bot-1' });
      if (apiPath.includes('/messages'))
        return Promise.resolve({
          messages: [
            {
              id: 'call-1',
              position: 1,
              senderType: 'bot',
              kind: 'tool',
              content:
                'mcp__botbox-desktop__browser_navigate\n{"url":"https://dunkel.tech"}',
              createdAt: '2026-11-16T11:00:00Z',
            },
            {
              id: 'result-1',
              position: 2,
              senderType: 'bot',
              kind: 'tool',
              content: 'navigated',
              createdAt: '2026-11-16T11:00:01Z',
            },
            {
              id: 'shot-1',
              position: 3,
              senderType: 'bot',
              kind: 'tool',
              content: `[Image: source: ${path}]\nbrowser screenshot`,
              createdAt: '2026-11-16T11:00:02Z',
            },
            {
              id: 'empty-1',
              position: 4,
              senderType: 'bot',
              kind: 'tool',
              content: '',
              createdAt: '2026-11-16T11:00:03Z',
            },
          ],
          turns: [],
        });
      return Promise.resolve({ budgets: [], spendTodayMicros: 0 });
    });
    vi.mocked(requestBlob).mockResolvedValue(
      new Blob([Uint8Array.from([137, 80, 78, 71])], { type: 'image/png' }),
    );
    // jsdom has no createObjectURL. Replacing URL entirely would also drop the
    // constructor, and page addresses would fall back to the generic sentence.
    const urls: string[] = [];
    const RealURL = URL;
    function TestURL(input: string | URL, base?: string | URL) {
      return new RealURL(input, base);
    }
    TestURL.prototype = RealURL.prototype;
    TestURL.createObjectURL = (blob: Blob) => {
      urls.push(blob.type);
      return 'blob:shot';
    };
    TestURL.revokeObjectURL = () => undefined;
    vi.stubGlobal('URL', TestURL);
    show(
      <ChatPanel
        workspaceId="ws-1"
        userId="user-1"
        botId="bot-1"
        name="Atlas"
        avatarSeed="atlas-seed"
      />,
    );
    expect(
      await screen.findByText('Opening https://dunkel.tech/'),
    ).toBeInTheDocument();
    expect(screen.getByText('Opened the page')).toBeInTheDocument();
    expect(screen.getByText('Browser screenshot')).toBeInTheDocument();
    expect(screen.getByText('Finished')).toBeInTheDocument();
    expect(screen.queryByText(/mcp__botbox/)).toBeNull();
    expect(screen.queryByText(/tool-results/)).toBeNull();
    expect(screen.queryByText('Tool call')).toBeNull();
    const image = await screen.findByRole('img', {
      name: 'Screenshot from the bot computer',
    });
    expect(image).toHaveAttribute('src', 'blob:shot');
    expect(urls).toEqual(['image/png']);
    expect(requestBlob).toHaveBeenCalledWith(
      '/api/workspaces/ws-1/conversations/conversation-1/messages/shot-1/tool-image',
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

describe('chat scroll behavior', () => {
  it('scrolls to the newest message, pauses auto-scroll while reading older messages, and preserves position on loadOlder', async () => {
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

    const initialMessages = Array.from({ length: 50 }, (_, index) => ({
      id: `msg-${index + 1}`,
      position: index + 51,
      senderType: 'bot',
      kind: 'text',
      content: `Message ${index + 51}`,
      createdAt: '2026-11-15T10:00:00Z',
    }));
    const olderMessages = [
      {
        id: 'msg-older-1',
        position: 50,
        senderType: 'user',
        kind: 'text',
        content: 'Older message 50',
        createdAt: '2026-11-15T09:59:00Z',
      },
    ];

    request.mockImplementation(
      (path: string, options?: { method?: string }) => {
        if (path.endsWith('/dm')) {
          return Promise.resolve({ id: 'conversation-1', botId: 'bot-1' });
        }
        if (path.includes('/messages?before=')) {
          return Promise.resolve({ messages: olderMessages, turns: [] });
        }
        if (path.includes('/messages') && options?.method === 'POST') {
          return Promise.resolve({ id: 'msg-sent' });
        }
        if (path.includes('/messages')) {
          return Promise.resolve({ messages: initialMessages, turns: [] });
        }
        return Promise.resolve({ spendTodayMicros: 0, budgets: [] });
      },
    );

    show(
      <ChatPanel
        workspaceId="ws-1"
        userId="user-1"
        botId="bot-1"
        name="Atlas"
        avatarSeed="atlas-seed"
      />,
    );

    const log = screen.getByRole('log', { name: messages.chat.history });
    let currentScrollHeight = 1200;
    Object.defineProperty(log, 'scrollHeight', {
      configurable: true,
      get: () =>
        log.textContent?.includes('Older message 50')
          ? 2300
          : currentScrollHeight,
    });
    Object.defineProperty(log, 'clientHeight', {
      configurable: true,
      get: () => 400,
    });

    await screen.findByText('Message 100');
    await waitFor(() =>
      expect(FakeSocket.current?.onmessage).toBeTypeOf('function'),
    );
    expect(log.scrollTop).toBe(1200);

    // Streaming delta while pinned at the bottom keeps scrolling down.
    currentScrollHeight = 1350;
    act(() => {
      FakeSocket.current.emit({ Kind: 'text_delta', Text: 'Streaming' });
    });
    expect(log.scrollTop).toBe(1350);

    // Negative path: scrolling up to read older messages disables auto-scroll.
    log.scrollTop = 200;
    fireEvent.scroll(log);
    currentScrollHeight = 1500;
    act(() => {
      FakeSocket.current.emit({ Kind: 'text_delta', Text: ' more text' });
    });
    expect(log.scrollTop).toBe(200);

    // Sending a message re-pins the view to the bottom.
    fireEvent.change(screen.getByLabelText(messages.chat.message), {
      target: { value: 'Follow up' },
    });
    fireEvent.submit(
      screen.getByRole('button', { name: messages.chat.send }).closest('form')!,
    );
    await waitFor(() => expect(log.scrollTop).toBe(1500));

    // Loading older messages preserves the reader's position without jumping to bottom.
    log.scrollTop = 100;
    fireEvent.scroll(log);
    fireEvent.click(screen.getByRole('button', { name: messages.chat.older }));
    expect(await screen.findByText('Older message 50')).toBeInTheDocument();
    expect(log.scrollTop).toBe(900);
  });
});
