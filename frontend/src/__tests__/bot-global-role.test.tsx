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
