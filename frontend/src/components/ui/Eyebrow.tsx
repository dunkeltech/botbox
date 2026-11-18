// SPDX-License-Identifier: AGPL-3.0-only

import { type ReactNode } from 'react';

export interface EyebrowProps {
  /** Label text rendered in uppercase after the // accent prefix. */
  children: ReactNode;
  className?: string;
}

const EYEBROW_PREFIX = '// ';

/**
 * Renders the dunkel.Tech section eyebrow (`// LABEL`) above page and card titles.
 */
export function Eyebrow({ children, className = '' }: EyebrowProps) {
  return (
    <p
      className={`text-[13px] font-bold uppercase leading-[1.4] tracking-[0.06em] text-muted ${className}`.trim()}
    >
      <span aria-hidden="true" className="font-black text-green">
        {EYEBROW_PREFIX}
      </span>
      {children}
    </p>
  );
}
