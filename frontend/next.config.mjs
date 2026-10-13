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
  return withNextIntl({
    ...nextConfig,
    distDir: phase === PHASE_DEVELOPMENT_SERVER ? '.next-dev' : '.next',
  });
}
