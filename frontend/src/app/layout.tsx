// SPDX-License-Identifier: AGPL-3.0-only

import type { Metadata } from 'next';
import { Lato } from 'next/font/google';
import { NextIntlClientProvider } from 'next-intl';
import { getMessages, getTranslations } from 'next-intl/server';
import { type ReactNode } from 'react';

import { QueryProvider } from '@/components/providers/QueryProvider';
import { AuthProvider } from '@/context/AuthContext';
import './globals.css';

const lato = Lato({
  subsets: ['latin'],
  weight: ['400', '700', '900'],
  variable: '--font-lato',
});

export async function generateMetadata(): Promise<Metadata> {
  const t = await getTranslations('metadata');
  return {
    title: t('title'),
    description: t('description'),
  };
}

interface RootLayoutProps {
  children: ReactNode;
}

/**
 * Root App Router layout configuring Lato font, next-intl messages, and TanStack Query.
 */
export default async function RootLayout({ children }: RootLayoutProps) {
  const messages = await getMessages();

  return (
    <html lang="en" className={lato.variable}>
      <body className="min-h-screen bg-paper-2 font-sans text-ink antialiased">
        <NextIntlClientProvider messages={messages}>
          <QueryProvider>
            <AuthProvider>{children}</AuthProvider>
          </QueryProvider>
        </NextIntlClientProvider>
      </body>
    </html>
  );
}
