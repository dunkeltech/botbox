import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from '@testing-library/react';
import { NextIntlClientProvider } from 'next-intl';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { BotGitLabInstancesPanel } from '@/components/settings/BotGitLabInstancesPanel';
import { GitLabInstancesPanel } from '@/components/settings/GitLabInstancesPanel';
import { requestJson } from '@/lib/api-client';
import type { WorkspaceRole } from '@/types/api';
import messages from '../../messages/en.json';

vi.mock('@/lib/api-client', () => ({ requestJson: vi.fn() }));
const request = vi.mocked(requestJson);
const instance = {
  name: 'Company GitLab',
  origin: 'https://gitlab.example.org:443',
};
function show(role: WorkspaceRole = 'owner') {
  return render(
    <NextIntlClientProvider locale="en" messages={messages}>
      <GitLabInstancesPanel workspaceId="ws-1" role={role} />
    </NextIntlClientProvider>,
  );
}

describe('GitLab instance configuration', () => {
  beforeEach(() => request.mockReset());
  afterEach(cleanup);
  it.each(['owner', 'admin'] as const)(
    'lets %s add and remove an exact instance',
    async (role) => {
      request
        .mockResolvedValueOnce({ instances: [] })
        .mockResolvedValueOnce(instance)
        .mockResolvedValueOnce(undefined);
      show(role);
      await screen.findByText('No self-hosted instances configured.');
      fireEvent.change(screen.getByLabelText('Instance name'), {
        target: { value: instance.name },
      });
      fireEvent.change(screen.getByLabelText('HTTPS origin'), {
        target: { value: 'https://gitlab.example.org' },
      });
      fireEvent.click(screen.getByRole('button', { name: 'Add instance' }));
      await screen.findByText(instance.origin);
      expect(request).toHaveBeenNthCalledWith(
        2,
        '/api/workspaces/ws-1/gitlab-instances',
        {
          method: 'POST',
          body: { name: instance.name, origin: 'https://gitlab.example.org' },
        },
      );
      fireEvent.click(
        screen.getByRole('button', { name: `Remove ${instance.name}` }),
      );
      await waitFor(() =>
        expect(screen.queryByText(instance.origin)).toBeNull(),
      );
      expect(request).toHaveBeenLastCalledWith(
        '/api/workspaces/ws-1/gitlab-instances',
        { method: 'DELETE', body: { origin: instance.origin } },
      );
    },
  );
  it.each(['member', 'viewer'] as const)(
    'shows %s the origin without management controls',
    async (role) => {
      request.mockResolvedValueOnce({ instances: [instance] });
      show(role);
      await screen.findByText(instance.origin);
      expect(screen.queryByRole('button')).toBeNull();
      expect(screen.queryByLabelText('HTTPS origin')).toBeNull();
    },
  );
  it('keeps the instance visible when removal fails', async () => {
    request
      .mockResolvedValueOnce({ instances: [instance] })
      .mockRejectedValueOnce(new Error('unavailable'));
    show();
    fireEvent.click(
      await screen.findByRole('button', { name: `Remove ${instance.name}` }),
    );
    await screen.findByRole('alert');
    expect(screen.getByText(instance.origin)).toBeTruthy();
  });
  it('reports loading errors', async () => {
    request.mockRejectedValueOnce(new Error('unavailable'));
    show();
    await screen.findByRole('alert');
  });
});

describe('GitLab bot assignments', () => {
  beforeEach(() => request.mockReset());
  afterEach(cleanup);
  function showBot(canManage = true) {
    return render(
      <NextIntlClientProvider locale="en" messages={messages}>
        <BotGitLabInstancesPanel
          workspaceId="ws-1"
          botId="bot-1"
          canManage={canManage}
        />
      </NextIntlClientProvider>,
    );
  }
  it('saves only explicitly selected instances for this bot', async () => {
    const second = {
      name: 'Second GitLab',
      origin: 'https://second.example.org:443',
    };
    request
      .mockResolvedValueOnce({ instances: [instance, second] })
      .mockResolvedValueOnce({ instances: [instance] })
      .mockResolvedValueOnce({ instances: [second] });
    showBot();
    const first = await screen.findByRole('checkbox', {
      name: /Company GitLab/,
    });
    const next = screen.getByRole('checkbox', { name: /Second GitLab/ });
    expect(first).toBeChecked();
    expect(next).not.toBeChecked();
    fireEvent.click(first);
    fireEvent.click(next);
    fireEvent.click(screen.getByRole('button', { name: 'Save GitLab access' }));
    await screen.findByText('GitLab access saved.');
    expect(request).toHaveBeenLastCalledWith(
      '/api/workspaces/ws-1/bots/bot-1/gitlab-instances',
      { method: 'PUT', body: { origins: [second.origin] } },
    );
  });
  it('can remove all bot assignments', async () => {
    request
      .mockResolvedValueOnce({ instances: [instance] })
      .mockResolvedValueOnce({ instances: [instance] })
      .mockResolvedValueOnce({ instances: [] });
    showBot();
    fireEvent.click(await screen.findByRole('checkbox'));
    fireEvent.click(screen.getByRole('button', { name: 'Save GitLab access' }));
    await screen.findByText('GitLab access saved.');
    expect(request).toHaveBeenLastCalledWith(
      '/api/workspaces/ws-1/bots/bot-1/gitlab-instances',
      { method: 'PUT', body: { origins: [] } },
    );
  });
  it('keeps read-only bot access read-only', async () => {
    request
      .mockResolvedValueOnce({ instances: [instance] })
      .mockResolvedValueOnce({ instances: [instance] });
    showBot(false);
    expect(await screen.findByRole('checkbox')).toBeDisabled();
    expect(screen.queryByRole('button')).toBeNull();
  });
  it('blocks saving when either configuration load fails', async () => {
    request
      .mockResolvedValueOnce({ instances: [instance] })
      .mockRejectedValueOnce(new Error('unavailable'));
    showBot();
    await screen.findByRole('alert');
    expect(
      screen.getByRole('button', { name: 'Save GitLab access' }),
    ).toBeDisabled();
  });
  it('reports a failed save without showing success', async () => {
    request
      .mockResolvedValueOnce({ instances: [instance] })
      .mockResolvedValueOnce({ instances: [] })
      .mockRejectedValueOnce(new Error('forbidden'));
    showBot();
    fireEvent.click(await screen.findByRole('checkbox'));
    fireEvent.click(screen.getByRole('button', { name: 'Save GitLab access' }));
    await screen.findByRole('alert');
    expect(screen.queryByText('GitLab access saved.')).toBeNull();
  });
});
