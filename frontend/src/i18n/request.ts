// SPDX-License-Identifier: AGPL-3.0-only

import { getRequestConfig } from 'next-intl/server';

export default getRequestConfig(async () => {
  const locale = 'en';

  return {
    locale,
    messages: (await import('../../messages/en.json')).default,
  };
});
