// SPDX-License-Identifier: AGPL-3.0-only

import { redirect } from 'next/navigation';

/**
 * Redirects the workspace settings root to the General & runtime section.
 */
export default function SettingsIndexPage() {
  redirect('/settings/general');
}
