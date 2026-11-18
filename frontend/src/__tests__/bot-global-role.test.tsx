// SPDX-License-Identifier: AGPL-3.0-only

import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from '@testing-library/react';
import { NextIntlClientProvider } from 'next-intl';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { BotBuilderForm } from '@/components/bots/BotBuilderForm';
import { createBot, updateBot } from '@/lib/api-client';
import type { Bot, WorkspaceSummary } from '@/types/api';
import messages from '../../messages/en.json';

vi.mock('next/navigation', () => ({ useRouter: () => ({ push: vi.fn() }) }));
vi.mock('@/context/AuthContext', () => ({
  useAuth: () => ({ user: { id: 'owner' } }),
}));
vi.mock('@/lib/api-client', () => ({
  createBot: vi.fn(),
  updateBot: vi.fn(),
  deleteBot: vi.fn(),
}));

const workspace: WorkspaceSummary = {
  id: 'ws',
  name: 'Team',
  slug: 'team',
  defaultDriver: 'docker',
  defaultLocale: 'en',
  role: 'owner',
  createdAt: '',
};
const bot: Bot = {
  id: 'bot',
  workspaceId: 'ws',
  ownerUserId: 'owner',
  name: 'Yoda',
  description: 'Check proposals for GDPR compliance.',
  avatarSeed: 'yoda',
  llmMode: 'cli',
  llmProvider: 'openai',
  llmModel: 'fixture',
  driverType: 'docker',
  replyLanguage: 'en',
  turnTimeoutMinutes: 10,
  status: 'idle',
  mood: 'idle',
  createdAt: '',
  updatedAt: '',
};

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe('global bot role', () => {
  it.each(['create', 'edit'] as const)(
    'saves free-form instructions on %s',
    async (mode) => {
      vi.mocked(createBot).mockResolvedValue(bot);
      vi.mocked(updateBot).mockResolvedValue(bot);
      render(
        <NextIntlClientProvider locale="en" messages={messages}>
          <BotBuilderForm mode={mode} workspace={workspace} bot={bot} />
        </NextIntlClientProvider>,
      );
      const role = screen.getByLabelText(messages.builder.descriptionLabel);
      expect(role).toHaveValue(bot.description);
      expect(role).toHaveAttribute('maxlength', '4000');
      expect(role).toHaveAccessibleDescription(
        messages.builder.descriptionHelp,
      );
      fireEvent.change(role, {
        target: {
          value:
            'Check GDPR compliance.\nSuggest privacy-preserving alternatives.',
        },
      });
      fireEvent.submit(role.closest('form') as HTMLFormElement);
      await waitFor(() => {
        const save = mode === 'create' ? createBot : updateBot;
        expect(save).toHaveBeenCalled();
        const instructions: unknown = expect.objectContaining({
          description:
            'Check GDPR compliance.\nSuggest privacy-preserving alternatives.',
        });
        if (mode === 'create') {
          expect(createBot).toHaveBeenCalledWith(workspace.id, instructions);
        } else {
          expect(updateBot).toHaveBeenCalledWith(
            workspace.id,
            bot.id,
            instructions,
          );
        }
      });
    },
  );

  it('keeps the global role read-only for viewers', () => {
    render(
      <NextIntlClientProvider locale="en" messages={messages}>
        <BotBuilderForm
          mode="edit"
          workspace={{ ...workspace, role: 'viewer' }}
          bot={bot}
        />
      </NextIntlClientProvider>,
    );
    expect(
      screen.getByLabelText(messages.builder.descriptionLabel),
    ).toBeDisabled();
    expect(updateBot).not.toHaveBeenCalled();
  });
});

describe('maximum bot turn duration', () => {
  it('loads the saved duration and saves a longer one in minutes', async () => {
    const configuredBot = { ...bot, turnTimeoutMinutes: 25 };
    vi.mocked(updateBot).mockResolvedValue({
      ...configuredBot,
      turnTimeoutMinutes: 45,
    });
    render(
      <NextIntlClientProvider locale="en" messages={messages}>
        <BotBuilderForm mode="edit" workspace={workspace} bot={configuredBot} />
      </NextIntlClientProvider>,
    );
    const duration = screen.getByRole('spinbutton', {
      name: messages.builder.turnTimeoutLabel,
    });
    expect(duration).toHaveValue(25);
    expect(duration).toHaveAccessibleDescription(
      messages.builder.turnTimeoutHelp,
    );
    fireEvent.change(duration, { target: { value: '45' } });
    fireEvent.click(
      screen.getByRole('button', { name: messages.builder.saveSubmit }),
    );
    expect(
      await screen.findByText(messages.builder.savedBanner),
    ).toBeInTheDocument();
    expect(updateBot).toHaveBeenCalledWith(
      workspace.id,
      bot.id,
      expect.objectContaining({ turnTimeoutMinutes: 45 }),
    );
  });

  it('defaults new bots to ten minutes and prevents invalid submissions', () => {
    render(
      <NextIntlClientProvider locale="en" messages={messages}>
        <BotBuilderForm mode="create" workspace={workspace} />
      </NextIntlClientProvider>,
    );
    fireEvent.change(screen.getByLabelText(messages.builder.nameLabel), {
      target: { value: 'New bot' },
    });
    const duration = screen.getByRole('spinbutton', {
      name: messages.builder.turnTimeoutLabel,
    });
    expect(duration).toHaveValue(10);
    const submit = screen.getByRole('button', {
      name: messages.builder.createSubmit,
    });
    for (const value of ['0', '121', '30.5', '']) {
      fireEvent.change(duration, { target: { value } });
      expect(duration).toBeInvalid();
      fireEvent.click(submit);
    }
    expect(createBot).not.toHaveBeenCalled();
  });

  it('disables the duration for viewers', () => {
    render(
      <NextIntlClientProvider locale="en" messages={messages}>
        <BotBuilderForm
          mode="edit"
          workspace={{ ...workspace, role: 'viewer' }}
          bot={bot}
        />
      </NextIntlClientProvider>,
    );
    expect(
      screen.getByRole('spinbutton', {
        name: messages.builder.turnTimeoutLabel,
      }),
    ).toBeDisabled();
  });
});
