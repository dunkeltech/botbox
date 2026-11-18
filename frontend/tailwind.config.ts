// SPDX-License-Identifier: AGPL-3.0-only

import type { Config } from 'tailwindcss';
import tokens from '../design/tokens.json';

const config: Config = {
  content: ['./src/**/*.{js,ts,jsx,tsx,mdx}'],
  theme: {
    extend: {
      colors: {
        ink: {
          DEFAULT: tokens.color.ink,
          2: tokens.color['ink-2'],
        },
        muted: tokens.color.muted,
        paper: {
          DEFAULT: tokens.color.paper,
          2: tokens.color['paper-2'],
        },
        line: {
          DEFAULT: tokens.color.line,
          strong: tokens.color['line-strong'],
        },
        green: {
          DEFAULT: tokens.color.green,
          600: tokens.color['green-600'],
          light: tokens.color['green-light'],
          logo: tokens.color['green-logo'],
        },
        mint: {
          DEFAULT: tokens.color.mint,
          soft: tokens.color['mint-soft'],
          line: tokens.color['mint-line'],
        },
        dark: {
          DEFAULT: tokens.color.dark,
          2: tokens.color['dark-2'],
        },
        terminal: tokens.color.terminal,
        blue: {
          DEFAULT: tokens.color.blue,
          tint: tokens.color['blue-tint'],
          line: tokens.color['blue-line'],
        },
        hex: {
          1: tokens.color['hex-1'],
          2: tokens.color['hex-2'],
          3: tokens.color['hex-3'],
        },
        amber: {
          DEFAULT: tokens.color.amber,
          tint: tokens.color['amber-tint'],
          line: tokens.color['amber-line'],
          'on-dark': tokens.color['amber-on-dark'],
        },
        'on-dark': {
          DEFAULT: tokens.color['on-dark'],
          muted: tokens.color['on-dark-muted'],
          faint: tokens.color['on-dark-faint'],
        },
      },
      borderRadius: {
        xs: `${tokens.radius.xs}px`,
        sm: `${tokens.radius.sm}px`,
        md: `${tokens.radius.md}px`,
        lg: `${tokens.radius.lg}px`,
        pill: `${tokens.radius.pill}px`,
      },
      boxShadow: {
        card: tokens.shadow.card,
        'card-hover': tokens.shadow.cardHover,
      },
      fontFamily: {
        sans: ['var(--font-lato)', 'system-ui', 'sans-serif'],
        mono: [
          'ui-monospace',
          'SFMono-Regular',
          'SF Mono',
          'Menlo',
          'Consolas',
          'Liberation Mono',
          'monospace',
        ],
      },
    },
  },
  plugins: [],
};

export default config;
