import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from '@testing-library/react';
import { NextIntlClientProvider } from 'next-intl';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { QueryProvider } from '@/components/providers/QueryProvider';
import BotSettingsPage from '@/app/bots/[botId]/settings/page';
import { RunnerControl } from '@/components/chat/RunnerControl';
import { useAuth } from '@/context/AuthContext';
import { getBot, requestJson } from '@/lib/api-client';
import type { Bot } from '@/types/api';
import messages from '../../messages/en.json';
vi.mock('@/lib/api-client', () => ({ getBot: vi.fn(), requestJson: vi.fn() }));
vi.mock('@/context/AuthContext', () => ({ useAuth: vi.fn() }));
vi.mock('next/navigation', () => ({ useParams: () => ({ botId: 'bot' }) }));
vi.mock('@/components/shell/AppShell', () => ({
  AppShell: ({ children }: { children: React.ReactNode }) => children,
}));
vi.mock('@/components/bots/BotBuilderForm', () => ({
  BotBuilderForm: () => null,
}));
vi.mock('@/components/settings/BotGitLabInstancesPanel', () => ({
  BotGitLabInstancesPanel: () => null,
}));
vi.mock('@/components/chat/BudgetPanel', () => ({ BudgetPanel: () => null }));
const request = vi.mocked(requestJson);
afterEach(() => {
  cleanup();
  vi.resetAllMocks();
});
function show(canManage = true) {
  return render(
    <NextIntlClientProvider locale="en" messages={messages}>
      <QueryProvider>
        <RunnerControl
          workspaceId="ws"
          botId="bot"
          userId="user"
          canManage={canManage}
        />
      </QueryProvider>
    </NextIntlClientProvider>,
  );
}
describe('runner control', () => {
  it('pauses and resumes with the persisted state', async () => {
    request
      .mockResolvedValueOnce({ paused: false })
      .mockResolvedValueOnce({ paused: true })
      .mockResolvedValueOnce({ paused: false });
    show();
    const pause = await screen.findByRole('button', { name: 'Pause bot' });
    await waitFor(() => expect(pause).toBeEnabled());
    fireEvent.click(pause);
    const resume = await screen.findByRole('button', { name: 'Resume bot' });
    expect(screen.getByText(/Queued messages keep/)).toBeInTheDocument();
    expect(request).toHaveBeenCalledWith(
      '/api/workspaces/ws/bots/bot/runtime',
      { method: 'PUT', body: { paused: true } },
    );
    await waitFor(() => expect(resume).toBeEnabled());
    fireEvent.click(resume);
    await screen.findByText('Bot ready');
    expect(request).toHaveBeenLastCalledWith(
      '/api/workspaces/ws/bots/bot/runtime',
      { method: 'PUT', body: { paused: false } },
    );
  });
  it('shows pause state without controls to viewers', async () => {
    request.mockResolvedValue({ paused: true });
    show(false);
    await screen.findByText('Bot paused');
    expect(screen.queryByRole('button')).not.toBeInTheDocument();
  });
  it('reloads committed state when cancellation publication fails', async () => {
    request
      .mockResolvedValueOnce({ paused: false })
      .mockRejectedValueOnce(new Error('queue down'))
      .mockResolvedValue({ paused: true });
    show();
    const pause = await screen.findByRole('button', { name: 'Pause bot' });
    await waitFor(() => expect(pause).toBeEnabled());
    fireEvent.click(pause);
    await screen.findByText('Bot paused');
    expect(screen.getByRole('alert')).toHaveTextContent('Could not update');
  });
  it.each([
    ['owner', 'user', true],
    ['admin', 'user', true],
    ['member', 'bot-owner', true],
    ['member', 'user', false],
    ['viewer', 'bot-owner', false],
  ] as const)(
    'exposes bot-wide pause in settings for %s as %s',
    async (role, userId, canManage) => {
      vi.mocked(useAuth).mockReturnValue({
        activeWorkspace: { id: 'ws', role },
        user: { id: userId },
      } as ReturnType<typeof useAuth>);
      vi.mocked(getBot).mockResolvedValue({
        id: 'bot',
        ownerUserId: 'bot-owner',
      } as Bot);
      request
        .mockResolvedValueOnce({ paused: false })
        .mockResolvedValueOnce({ paused: true });
      render(
        <NextIntlClientProvider locale="en" messages={messages}>
          <QueryProvider>
            <BotSettingsPage />
          </QueryProvider>
        </NextIntlClientProvider>,
      );
      await screen.findByText('Bot ready');
      expect(
        screen.getByRole('heading', { name: 'Bot activity' }),
      ).toBeInTheDocument();
      if (canManage) {
        fireEvent.click(screen.getByRole('button', { name: 'Pause bot' }));
        await screen.findByRole('button', { name: 'Resume bot' });
        expect(request).toHaveBeenCalledWith(
          '/api/workspaces/ws/bots/bot/runtime',
          {
            method: 'PUT',
            body: { paused: true },
          },
        );
      } else {
        expect(
          screen.queryByRole('button', { name: 'Pause bot' }),
        ).not.toBeInTheDocument();
      }
    },
  );
});
