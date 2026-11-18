// SPDX-License-Identifier: AGPL-3.0-only

import { cleanup, render, screen } from '@testing-library/react';
import { NextIntlClientProvider } from 'next-intl';
import { afterEach, describe, expect, it } from 'vitest';

import { BotAvatar } from '@/components/ui/BotAvatar';
import {
  computeAvatarSpec,
  fnv1a32,
  generateRandomAvatarSeed,
  isValidAvatarSeed,
} from '@/lib/avatar';
import { type AvatarMood } from '@/types/api';
import fixtures from '../../../design/avatar_fixtures.json';
import enMessages from '../../messages/en.json';

afterEach(() => {
  cleanup();
});

describe('Robot avatar generator parity & rendering', () => {
  it('matches every Go/design fixture vector for FNV-1a and avatar geometry', () => {
    expect(fixtures.length).toBeGreaterThanOrEqual(11);

    for (const vec of fixtures) {
      expect(fnv1a32(vec.seed)).toBe(vec.hash);
      const spec = computeAvatarSpec(vec.seed);
      expect(spec).toEqual(vec);
    }
  });

  it('validates seeds and generates compliant random seeds', () => {
    expect(isValidAvatarSeed('atlas-005a')).toBe(true);
    expect(isValidAvatarSeed('')).toBe(false);
    expect(isValidAvatarSeed('Atlas-Upper')).toBe(false);
    expect(isValidAvatarSeed('invalid space')).toBe(false);

    const generated = generateRandomAvatarSeed('Atlas Bot!');
    expect(isValidAvatarSeed(generated)).toBe(true);
    expect(generated).toMatch(/^atlas-bot-[0-9a-f]{4}$/);
  });

  it('renders all 4 mood states with accessible labels or decorative mode', () => {
    const moods: AvatarMood[] = ['idle', 'thinking', 'action', 'offline'];

    for (const mood of moods) {
      const { unmount } = render(
        <NextIntlClientProvider locale="en" messages={enMessages}>
          <BotAvatar seed="ferrit-00e5" mood={mood} showStatusDot />
        </NextIntlClientProvider>,
      );

      const img = screen.getByRole('img', {
        name: `Robot avatar ferrit-00e5, ${mood}`,
      });
      expect(img).toBeInTheDocument();

      if (mood === 'thinking') {
        expect(
          img.querySelector('[data-avatar-part="thinking-dots"]'),
        ).not.toBeNull();
      }
      if (mood === 'action') {
        expect(img.querySelector('[data-avatar-part="waves"]')).not.toBeNull();
      }
      unmount();
    }

    const { container } = render(
      <NextIntlClientProvider locale="en" messages={enMessages}>
        <BotAvatar seed="atlas-005a" decorative />
      </NextIntlClientProvider>,
    );
    const svg = container.querySelector('svg');
    expect(svg).toHaveAttribute('aria-hidden', 'true');
  });
});
