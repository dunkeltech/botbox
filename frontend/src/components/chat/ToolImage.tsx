// SPDX-License-Identifier: AGPL-3.0-only

'use client';

import { useEffect, useState } from 'react';
import { requestBlob } from '@/lib/api-client';

/**
 * Fetches a protected tool screenshot as a Blob object URL so credentials never
 * appear in image URLs and object URLs are revoked on unmount.
 */
export function ToolImage({
  path,
  alt,
  className = 'mt-2 max-h-64 rounded-sm border border-line',
}: {
  path: string;
  alt: string;
  className?: string;
}) {
  const [src, setSrc] = useState<string | null>(null);
  useEffect(() => {
    let url: string | null = null;
    let active = true;
    void requestBlob(path)
      .then((blob) => {
        if (!active) return;
        url = URL.createObjectURL(blob);
        setSrc(url);
      })
      .catch(() => {
        if (active) setSrc(null);
      });
    return () => {
      active = false;
      if (url) URL.revokeObjectURL(url);
    };
  }, [path]);
  if (!src) return null;
  // Tool screenshots come from an authenticated fetch into an object URL.
  // eslint-disable-next-line @next/next/no-img-element
  return <img src={src} alt={alt} className={className} />;
}
