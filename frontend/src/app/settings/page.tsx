import { redirect } from 'next/navigation';

/**
 * Redirects the workspace settings root to the General & runtime section.
 */
export default function SettingsIndexPage() {
  redirect('/settings/general');
}
