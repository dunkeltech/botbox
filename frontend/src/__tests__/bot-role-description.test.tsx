import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
} from '@testing-library/react';
import { NextIntlClientProvider } from 'next-intl';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { DirectDetails } from '@/components/team/GroupDetails';
import type { Bot } from '@/types/api';
import messages from '../../messages/en.json';

const bot: Bot = {
  id: 'bot-1',
  workspaceId: 'ws-1',
  name: 'Atlas',
  description: '',
  avatarSeed: 'atlas',
  llmMode: 'api',
  llmProvider: 'openai',
  llmModel: 'fixture',
  driverType: 'docker',
  replyLanguage: 'en',
  status: 'idle',
  mood: 'idle',
  createdAt: '',
  updatedAt: '',
};

let contentHeight = 60;
let resize: () => void;
const disconnect = vi.fn();

function details(description: string, id = bot.id) {
  return (
    <NextIntlClientProvider locale="en" messages={messages}>
      <DirectDetails
        bot={{ ...bot, id, description }}
        groups={[]}
        canCreate={false}
        onOpenGroup={vi.fn()}
        onNewGroup={vi.fn()}
      />
    </NextIntlClientProvider>
  );
}

beforeEach(() => {
  contentHeight = 60;
  vi.spyOn(HTMLElement.prototype, 'scrollHeight', 'get').mockImplementation(
    () => contentHeight,
  );
  vi.spyOn(HTMLElement.prototype, 'clientHeight', 'get').mockReturnValue(93);
  vi.stubGlobal(
    'ResizeObserver',
    class {
      constructor(callback: () => void) {
        resize = callback;
      }
      observe = vi.fn();
      disconnect = disconnect;
    },
  );
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  vi.clearAllMocks();
});

describe('bot role in team chat details', () => {
  it('renders Markdown rather than literal formatting characters', () => {
    render(
      details(
        '## Responsibilities\n\nReview **privacy** and `code`.\n\n- Check consent',
      ),
    );
    expect(
      screen.getByRole('heading', { name: 'Responsibilities' }),
    ).toBeInTheDocument();
    expect(screen.getByText('privacy').tagName).toBe('STRONG');
    expect(screen.getByText('code').tagName).toBe('CODE');
    expect(screen.getByText('Check consent').tagName).toBe('LI');
    expect(
      screen.queryByRole('button', { name: 'More' }),
    ).not.toBeInTheDocument();
  });

  it('expands and collapses an overflowing role without losing Markdown', () => {
    contentHeight = 400;
    render(
      details('**Review privacy**\n\n' + 'Detailed instructions. '.repeat(100)),
    );
    const more = screen.getByRole('button', { name: 'More' });
    const preview = document.getElementById(
      more.getAttribute('aria-controls')!,
    );
    expect(preview).toHaveClass('max-h-[6.2em]', 'overflow-hidden');
    expect(more).toHaveAttribute('aria-expanded', 'false');
    fireEvent.click(more);
    expect(screen.getByRole('button', { name: 'Less' })).toHaveAttribute(
      'aria-expanded',
      'true',
    );
    expect(preview).not.toHaveClass('max-h-[6.2em]');
    expect(screen.getByText('Review privacy').tagName).toBe('STRONG');
    expect(preview).toHaveTextContent(
      'Detailed instructions. '.repeat(100).trim(),
    );
    fireEvent.click(screen.getByRole('button', { name: 'Less' }));
    expect(preview).toHaveClass('max-h-[6.2em]');
    expect(screen.getByRole('button', { name: 'More' })).toHaveAttribute(
      'aria-expanded',
      'false',
    );
  });

  it.each(['', '  \n  '])('omits empty roles (%j)', (description) => {
    render(details(description));
    expect(
      screen.queryByRole('region', { name: messages.builder.descriptionLabel }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole('button', { name: 'More' }),
    ).not.toBeInTheDocument();
  });

  it('rechecks overflow when the panel width changes and disconnects on unmount', () => {
    const { unmount } = render(details('Privacy review'));
    expect(
      screen.queryByRole('button', { name: 'More' }),
    ).not.toBeInTheDocument();
    contentHeight = 200;
    act(() => resize());
    expect(screen.getByRole('button', { name: 'More' })).toBeInTheDocument();
    contentHeight = 60;
    act(() => resize());
    expect(
      screen.queryByRole('button', { name: 'More' }),
    ).not.toBeInTheDocument();
    unmount();
    expect(disconnect).toHaveBeenCalled();
  });

  it.each(['bot', 'description'])('collapses when the %s changes', (change) => {
    contentHeight = 400;
    const { rerender } = render(details('Long **role**'));
    fireEvent.click(screen.getByRole('button', { name: 'More' }));
    rerender(
      details(
        change === 'description' ? 'Updated **role**' : 'Long **role**',
        change === 'bot' ? 'bot-2' : bot.id,
      ),
    );
    expect(screen.getByRole('button', { name: 'More' })).toHaveAttribute(
      'aria-expanded',
      'false',
    );
  });

  it('keeps the full role reachable without ResizeObserver', () => {
    vi.stubGlobal('ResizeObserver', undefined);
    render(details('Privacy review'));
    contentHeight = 200;
    fireEvent(window, new Event('resize'));
    fireEvent.click(screen.getByRole('button', { name: 'More' }));
    expect(screen.getByRole('button', { name: 'Less' })).toHaveAttribute(
      'aria-expanded',
      'true',
    );
  });

  it('reveals focused Markdown controls for keyboard users', () => {
    contentHeight = 400;
    render(details('Long role\n\n[Policy](https://example.com/policy)'));
    fireEvent.focus(screen.getByRole('link', { name: 'Policy' }));
    expect(screen.getByRole('button', { name: 'Less' })).toHaveAttribute(
      'aria-expanded',
      'true',
    );
  });

  it('uses the safe renderer for HTML, unsafe links and external images', () => {
    const { container } = render(
      details(
        '<script>alert(1)</script>\n\n[Unsafe](javascript:alert%281%29)\n\n![Diagram](https://example.com/diagram.png)',
      ),
    );
    expect(container.querySelector('script')).toBeNull();
    expect(container.querySelector('img')).toBeNull();
    expect(
      screen.queryByRole('link', { name: 'Unsafe' }),
    ).not.toBeInTheDocument();
    expect(screen.getByRole('link', { name: /Diagram/ })).toHaveAttribute(
      'href',
      'https://example.com/diagram.png',
    );
  });
});
