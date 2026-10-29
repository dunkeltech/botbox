import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react';
import { NextIntlClientProvider } from 'next-intl';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { ChatPanel } from '@/components/chat/ChatPanel';
import {
  MarkdownMessage,
  safeChatUrl,
} from '@/components/chat/MarkdownMessage';
import { QueryProvider } from '@/components/providers/QueryProvider';
import { GroupConversation } from '@/components/team/GroupConversation';
import { requestJson } from '@/lib/api-client';
import messages from '../../messages/en.json';

vi.mock('@/lib/api-client', async () => {
  const actual =
    await vi.importActual<typeof import('@/lib/api-client')>(
      '@/lib/api-client',
    );
  return {
    ...actual,
    requestJson: vi.fn(),
    getAccessToken: vi.fn(() => null),
  };
});

const mockedRequestJson = vi.mocked(requestJson);

function renderMarkdown(
  content: string,
  options?: { tone?: 'light' | 'dark'; mentionNames?: string[] },
) {
  return render(
    <NextIntlClientProvider locale="en" messages={messages}>
      <MarkdownMessage
        content={content}
        tone={options?.tone}
        mentionNames={options?.mentionNames}
      />
    </NextIntlClientProvider>,
  );
}

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
  vi.unstubAllGlobals();
});

describe('safeChatUrl', () => {
  it('allows only http, https, and optional mailto URLs', () => {
    expect(safeChatUrl('https://dunkel.tech/docs')).toBe(
      'https://dunkel.tech/docs',
    );
    expect(safeChatUrl('http://example.com')).toBe('http://example.com');
    expect(safeChatUrl('mailto:team@example.com')).toBe(
      'mailto:team@example.com',
    );
    expect(safeChatUrl('mailto:team@example.com', false)).toBeNull();
    expect(safeChatUrl('javascript:alert(1)')).toBeNull();
    expect(safeChatUrl('data:text/html,<script>alert(1)</script>')).toBeNull();
    expect(safeChatUrl('vbscript:msgbox(1)')).toBeNull();
    expect(safeChatUrl('/relative/path')).toBeNull();
    expect(safeChatUrl('https://example.com/\u0000bad')).toBeNull();
  });
});

describe('MarkdownMessage', () => {
  it('renders CommonMark and GFM blocks including headings, lists, task lists, blockquotes, and scrollable tables', () => {
    const md = [
      '# Release Summary',
      '',
      'Line one\nLine two with **bold**, *italic*, ~~old~~, and `npm test`.',
      '',
      '- First item',
      '- [x] Completed check',
      '',
      '> Verify before deploy',
      '',
      '| Service | Status |',
      '| --- | --- |',
      '| api | green |',
    ].join('\n');

    const { container } = renderMarkdown(md);

    expect(
      screen.getByRole('heading', { level: 1, name: 'Release Summary' }),
    ).toBeInTheDocument();
    expect(container.querySelector('p br')).not.toBeNull();
    expect(container.querySelector('strong')).toHaveTextContent('bold');
    expect(container.querySelector('em')).toHaveTextContent('italic');
    expect(container.querySelector('del')).toHaveTextContent('old');
    expect(container.querySelector('code')).toHaveTextContent('npm test');
    expect(screen.getByText('First item')).toBeInTheDocument();

    const checkbox = screen.getByRole('checkbox');
    expect(checkbox).toBeChecked();
    expect(checkbox).toBeDisabled();

    expect(container.querySelector('blockquote')).toHaveTextContent(
      'Verify before deploy',
    );

    const tableRegion = screen.getByRole('region', {
      name: messages.chat.markdownTable,
    });
    expect(tableRegion).toHaveAttribute('tabindex', '0');
    expect(within(tableRegion).getByRole('table')).toBeInTheDocument();
    expect(
      within(tableRegion).getByRole('columnheader', { name: 'Service' }),
    ).toBeInTheDocument();
    expect(
      within(tableRegion).getByRole('cell', { name: 'green' }),
    ).toBeInTheDocument();
  });

  it('renders fenced code blocks in terminal style with accessible copy feedback and failure handling', async () => {
    const writeText = vi
      .fn<(text: string) => Promise<void>>()
      .mockResolvedValueOnce(undefined)
      .mockRejectedValueOnce(new Error('Clipboard blocked'));

    vi.stubGlobal('navigator', {
      clipboard: { writeText },
    });

    const { container } = renderMarkdown('```ts\nconst answer = 42;\n```');

    expect(screen.getByText('ts')).toBeInTheDocument();
    const pre = container.querySelector('pre');
    expect(pre).not.toBeNull();
    expect(pre).toHaveAttribute('tabindex', '0');
    expect(pre).toHaveTextContent('const answer = 42;');

    const copyBtn = screen.getByRole('button', {
      name: messages.chat.copyCode,
    });
    expect(copyBtn).toHaveAttribute('type', 'button');

    fireEvent.click(copyBtn);
    await waitFor(() =>
      expect(writeText).toHaveBeenCalledWith('const answer = 42;'),
    );
    expect(await screen.findByRole('status')).toHaveTextContent(
      messages.chat.copiedCode,
    );
    expect(copyBtn).toBeEnabled();

    fireEvent.click(copyBtn);
    await waitFor(() => expect(writeText).toHaveBeenCalledTimes(2));
    expect(await screen.findByRole('status')).toHaveTextContent(
      messages.chat.copyCodeFailed,
    );
    expect(copyBtn).toBeEnabled();
  });

  it('allows safe external links, blocks unsafe URL schemes, and turns markdown images into links', () => {
    const md = [
      '[Docs](https://dunkel.tech)',
      '[Mail](mailto:hi@dunkel.tech)',
      '[BadJS](javascript:alert(1))',
      '[BadData](data:text/html,xss)',
      '![Architecture](https://dunkel.tech/diagram.png)',
      '![BlockedImage](javascript:alert(2))',
      '[![Badge](https://dunkel.tech/badge.png)](https://dunkel.tech/build)',
    ].join('\n\n');

    const { container } = renderMarkdown(md);

    const docsLink = screen.getByRole('link', { name: 'Docs' });
    expect(docsLink).toHaveAttribute('href', 'https://dunkel.tech');
    expect(docsLink).toHaveAttribute('target', '_blank');
    expect(docsLink).toHaveAttribute('rel', 'noopener noreferrer');

    const mailLink = screen.getByRole('link', { name: 'Mail' });
    expect(mailLink).toHaveAttribute('href', 'mailto:hi@dunkel.tech');

    expect(screen.getByText('BadJS')).toBeInTheDocument();
    expect(screen.queryByRole('link', { name: 'BadJS' })).toBeNull();
    expect(screen.getByText('BadData')).toBeInTheDocument();
    expect(screen.queryByRole('link', { name: 'BadData' })).toBeNull();

    expect(container.querySelector('img')).toBeNull();
    const imgLink = screen.getByRole('link', { name: 'Architecture' });
    expect(imgLink).toHaveAttribute('href', 'https://dunkel.tech/diagram.png');
    expect(imgLink).toHaveAttribute('target', '_blank');

    expect(screen.getByText('BlockedImage')).toBeInTheDocument();
    expect(screen.queryByRole('link', { name: 'BlockedImage' })).toBeNull();

    const nestedOuter = screen.getByRole('link', { name: 'Badge' });
    expect(nestedOuter).toHaveAttribute('href', 'https://dunkel.tech/build');
    expect(nestedOuter.querySelector('a')).toBeNull();
  });

  it('never interprets raw HTML or scripts inside markdown', () => {
    const first = renderMarkdown('<script>unsafe()</script>');
    expect(first.container.querySelector('script')).toBeNull();
    expect(screen.getByText('<script>unsafe()</script>')).toBeInTheDocument();
    first.unmount();

    const second = renderMarkdown(
      'Inline <img src="x" onerror="alert(1)"> and <iframe src="https://evil.test"></iframe>',
    );
    expect(second.container.querySelector('img')).toBeNull();
    expect(second.container.querySelector('iframe')).toBeNull();
    expect(second.container.textContent).toContain('<img src="x"');
    expect(second.container.textContent).toContain('<iframe');
  });

  it('highlights roster mentions in prose only and skips inline code and fenced code blocks', () => {
    const md = [
      'Please check with @Atlas on this.',
      '',
      'Do not trigger `@Atlas` in inline code.',
      '',
      '```bash',
      'echo "@Atlas"',
      '```',
    ].join('\n');

    const { container } = renderMarkdown(md, {
      mentionNames: ['Atlas'],
      tone: 'dark',
    });

    const mentions = container.querySelectorAll('[data-mention="true"]');
    expect(mentions).toHaveLength(1);
    expect(mentions[0]).toHaveTextContent('@Atlas');
    expect(mentions[0]).toHaveClass('text-mint');
  });

  it('handles incomplete streaming markdown gracefully', () => {
    const { container } = renderMarkdown('Running checks...\n```sh\nnpm test');
    expect(screen.getByText('Running checks...')).toBeInTheDocument();
    expect(container.querySelector('pre')).toHaveTextContent('npm test');
  });
});

describe('Markdown integration in chat views', () => {
  it('renders markdown in user bubbles, bot messages, and live streams in ChatPanel while keeping tool messages safe', async () => {
    mockedRequestJson.mockImplementation((path: string) => {
      if (path.endsWith('/dm')) {
        return Promise.resolve({ id: 'conv-1', botId: 'bot-1' });
      }
      if (path.includes('/messages')) {
        return Promise.resolve({
          messages: [
            {
              id: 'm1',
              position: 1,
              senderType: 'user',
              kind: 'text',
              content: 'Check **logs** at [portal](https://example.com)',
              createdAt: '2026-11-17T10:00:00Z',
            },
            {
              id: 'm2',
              position: 2,
              senderType: 'bot',
              kind: 'text',
              content: 'Done:\n\n- `tests` green',
              createdAt: '2026-11-17T10:00:05Z',
            },
            {
              id: 'm3',
              position: 3,
              senderType: 'bot',
              kind: 'tool',
              content: 'screenshot\n{}',
              createdAt: '2026-11-17T10:00:06Z',
            },
          ],
          turns: [],
        });
      }
      return Promise.resolve({ spendTodayMicros: 0, budgets: [] });
    });

    const { container } = render(
      <NextIntlClientProvider locale="en" messages={messages}>
        <QueryProvider>
          <ChatPanel
            workspaceId="ws-1"
            userId="user-1"
            botId="bot-1"
            name="Atlas"
            avatarSeed="atlas-seed"
            variant="session"
          />
        </QueryProvider>
      </NextIntlClientProvider>,
    );

    expect(await screen.findByText('logs')).toBeInTheDocument();
    expect(container.querySelector('strong')).toHaveTextContent('logs');
    expect(screen.getByRole('link', { name: 'portal' })).toHaveClass(
      'text-mint',
    );
    expect(container.querySelector('code')).toHaveTextContent('tests');
    // Tool calls no longer render inline in chat; they collapse to a turn chip
    // and open in the tool calls trace on demand.
    expect(screen.queryByText('Taking a screenshot of the desktop')).toBeNull();
    const chip = screen.getByRole('button', { name: /1 tool step/i });
    fireEvent.click(chip);
    expect(
      await screen.findByText('Taking a screenshot of the desktop'),
    ).toBeInTheDocument();
  });

  it('renders markdown and roster mentions together in GroupConversation', async () => {
    mockedRequestJson.mockImplementation((path: string) => {
      if (path.includes('/approvals')) {
        return Promise.resolve({ approvals: [] });
      }
      if (path.includes('/messages')) {
        return Promise.resolve({
          messages: [
            {
              id: 'gm-1',
              position: 1,
              senderType: 'user',
              senderUserId: 'user-1',
              kind: 'text',
              content: '**Heads up** @Atlas: see [build](https://example.com)',
              createdAt: '2026-11-17T10:00:00Z',
            },
          ],
          turns: [],
        });
      }
      return Promise.resolve({});
    });

    const { container } = render(
      <NextIntlClientProvider locale="en" messages={messages}>
        <QueryProvider>
          <GroupConversation
            workspaceId="ws-1"
            userId="user-1"
            role="member"
            canPost={true}
            query=""
            group={{
              id: 'g-1',
              name: 'Launch',
              goal: 'Ship',
              leadBotId: 'bot-1',
              botTalk: false,
              turnLimit: 20,
              answerPolicy: 'mentioned_else_lead',
              askBefore: [],
              budgetLimitMicros: null,
              paused: false,
              pauseReason: '',
              turnsSinceHuman: 0,
              unread: 0,
              members: [
                { type: 'bot', id: 'bot-1', roleText: 'lead', name: 'Atlas' },
                { type: 'user', id: 'user-1', roleText: '', name: 'Jan' },
              ],
            }}
          />
        </QueryProvider>
      </NextIntlClientProvider>,
    );

    expect(await screen.findByText('Heads up')).toBeInTheDocument();
    expect(container.querySelector('strong')).toHaveTextContent('Heads up');
    const mention = container.querySelector('[data-mention="true"]');
    expect(mention).toHaveTextContent('@Atlas');
    expect(screen.getByRole('link', { name: 'build' })).toHaveAttribute(
      'href',
      'https://example.com',
    );
  });
});
