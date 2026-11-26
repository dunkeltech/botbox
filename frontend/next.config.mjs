// SPDX-License-Identifier: AGPL-3.0-only

import process from 'node:process';
import { PHASE_DEVELOPMENT_SERVER } from 'next/constants.js';
import createNextIntlPlugin from 'next-intl/plugin';

const withNextIntl = createNextIntlPlugin('./src/i18n/request.ts');

const apiBackendUrl = (
  process.env.API_BACKEND_URL ?? 'http://127.0.0.1:8080'
).replace(/\/$/, '');

/** @type {import('next').NextConfig} */
const nextConfig = {
  reactStrictMode: true,
  output: process.env.BOTBOX_STANDALONE === '1' ? 'standalone' : undefined,
  // eslint-disable-next-line @typescript-eslint/require-await -- NextConfig.rewrites must return a Promise.
  async rewrites() {
    return [
      {
        source: '/api/:path*',
        destination: `${apiBackendUrl}/api/:path*`,
      },
      // The desktop viewer opens a WebSocket on the page origin. Without this
      // rewrite, Next answers it itself and noVNC stays on "Connecting…".
      // Caddy already forwards /ws when the browser uses port 8000.
      {
        source: '/ws/:path*',
        destination: `${apiBackendUrl}/ws/:path*`,
      },
    ];
  },
};

/**
 * Keeps a running development server separate from production build output.
 * @param {string} phase
 */
export default function configureNext(phase) {
  const isDev = phase === PHASE_DEVELOPMENT_SERVER;
  const scriptSrc = isDev
    ? "script-src 'self' 'unsafe-inline' 'unsafe-eval'"
    : "script-src 'self' 'unsafe-inline'";
  const csp = [
    "default-src 'self'",
    scriptSrc,
    "style-src 'self' 'unsafe-inline'",
    "img-src 'self' data: blob:",
    "font-src 'self' data:",
    "connect-src 'self' ws: wss:",
    "frame-ancestors 'none'",
    "object-src 'none'",
    "base-uri 'self'",
    "form-action 'self'",
  ].join('; ');

  return withNextIntl({
    ...nextConfig,
    // eslint-disable-next-line @typescript-eslint/require-await -- NextConfig.headers must return a Promise.
    async headers() {
      return [
        {
          source: '/:path*',
          headers: [
            { key: 'Content-Security-Policy', value: csp },
            { key: 'X-Content-Type-Options', value: 'nosniff' },
            { key: 'X-Frame-Options', value: 'DENY' },
            {
              key: 'Referrer-Policy',
              value: 'strict-origin-when-cross-origin',
            },
            {
              key: 'Permissions-Policy',
              value: 'camera=(), microphone=(), geolocation=()',
            },
          ],
        },
      ];
    },
    distDir: isDev ? '.next-dev' : '.next',
  });
}
