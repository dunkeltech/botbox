import process from 'node:process';
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
    ];
  },
};

export default withNextIntl(nextConfig);
