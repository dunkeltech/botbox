// SPDX-License-Identifier: AGPL-3.0-only

import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from '@testing-library/react';
import { NextIntlClientProvider } from 'next-intl';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import type { ReactNode } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { BotBuilderForm } from '@/components/bots/BotBuilderForm';
import { createBot, listModelPrices, updateBot } from '@/lib/api-client';
import type { Bot, WorkspaceSummary } from '@/types/api';
import messages from '../../messages/en.json';

vi.mock('next/navigation', () => ({ useRouter: () => ({ push: vi.fn() }) }));
vi.mock('@/context/AuthContext', () => ({
  useAuth: () => ({ user: { id: 'owner' } }),
}));
vi.mock('@/lib/api-client', () => ({
  createBot: vi.fn(),
  listModelPrices: vi.fn(),
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

function renderBuilder(children: ReactNode) {
  return render(
    <NextIntlClientProvider locale="en" messages={messages}>
      <QueryClientProvider client={new QueryClient()}>
        {children}
      </QueryClientProvider>
    </NextIntlClientProvider>,
  );
}

beforeEach(() => {
  vi.mocked(listModelPrices).mockResolvedValue({
    prices: [
      {
        provider: 'openai',
        model: 'fixture',
        inputMicrosPerMillion: 0,
        outputMicrosPerMillion: 0,
        cacheMicrosPerMillion: 0,
      },
    ],
  });
});

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
      renderBuilder(
        <BotBuilderForm mode={mode} workspace={workspace} bot={bot} />,
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
      if (mode === 'create') {
        await waitFor(() =>
          expect(screen.getByRole('option', { name: 'fixture' })).toBeEnabled(),
        );
      }
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
    renderBuilder(
      <BotBuilderForm
        mode="edit"
        workspace={{ ...workspace, role: 'viewer' }}
        bot={bot}
      />,
    );
    expect(
      screen.getByLabelText(messages.builder.descriptionLabel),
    ).toBeDisabled();
    expect(updateBot).not.toHaveBeenCalled();
  });

  it('shows a saved unpriced model as unavailable and only lets an owner select a priced replacement', async () => {
    vi.mocked(listModelPrices).mockResolvedValue({
      prices: [
        {
          provider: 'openai',
          model: 'replacement',
          inputMicrosPerMillion: 0,
          outputMicrosPerMillion: 0,
          cacheMicrosPerMillion: 0,
        },
      ],
    });
    vi.mocked(updateBot).mockResolvedValue({ ...bot, llmModel: 'replacement' });
    renderBuilder(
      <BotBuilderForm
        mode="edit"
        workspace={workspace}
        bot={{
          ...bot,
          missingModelPrice: { provider: 'openai', model: 'fixture' },
        }}
      />,
    );
    const model = screen.getByRole('combobox', {
      name: messages.builder.modelLabel,
    });
    expect(
      await screen.findByRole('option', { name: 'replacement' }),
    ).toBeEnabled();
    expect(screen.getByRole('option', { name: 'fixture' })).toBeDisabled();
    expect(model).toHaveValue('fixture');
    expect(
      screen.getByText(/requires a model price for openai \/ fixture/),
    ).toBeVisible();
    fireEvent.change(model, { target: { value: 'replacement' } });
    fireEvent.click(
      screen.getByRole('button', { name: messages.builder.saveSubmit }),
    );
    await waitFor(() =>
      expect(updateBot).toHaveBeenCalledWith(
        workspace.id,
        bot.id,
        expect.objectContaining({
          llmProvider: 'openai',
          llmModel: 'replacement',
        }),
      ),
    );
  });
});

describe('maximum bot turn duration', () => {
  it('loads the saved duration and saves a longer one in minutes', async () => {
    const configuredBot = { ...bot, turnTimeoutMinutes: 25 };
    vi.mocked(updateBot).mockResolvedValue({
      ...configuredBot,
      turnTimeoutMinutes: 45,
    });
    renderBuilder(
      <BotBuilderForm mode="edit" workspace={workspace} bot={configuredBot} />,
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
    renderBuilder(<BotBuilderForm mode="create" workspace={workspace} />);
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
    renderBuilder(
      <BotBuilderForm
        mode="edit"
        workspace={{ ...workspace, role: 'viewer' }}
        bot={bot}
      />,
    );
    expect(
      screen.getByRole('spinbutton', {
        name: messages.builder.turnTimeoutLabel,
      }),
    ).toBeDisabled();
  });
});
