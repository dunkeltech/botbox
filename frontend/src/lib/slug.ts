// SPDX-License-Identifier: AGPL-3.0-only

/**
 * Slugifies a human-readable workspace name into a URL-safe lowercase slug.
 * Mirrors backend workspace slug constraints (lowercase ASCII letters, digits, hyphens).
 */
export function slugifyName(name: string): string {
  return name
    .toLowerCase()
    .trim()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 48);
}
