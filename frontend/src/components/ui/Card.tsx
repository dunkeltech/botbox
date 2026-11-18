// SPDX-License-Identifier: AGPL-3.0-only

import { type HTMLAttributes, type ReactNode } from 'react';

export interface CardProps extends HTMLAttributes<HTMLDivElement> {
  children: ReactNode;
  interactive?: boolean;
}

/**
 * Surface container using STYLEGUIDE 14 px corners, line border, and card elevation.
 */
export function Card({
  children,
  interactive = false,
  className = '',
  ...rest
}: CardProps) {
  const interactiveClasses = interactive
    ? 'transition duration-150 ease-out hover:-translate-y-1 hover:border-green-light hover:shadow-card-hover'
    : '';

  return (
    <div
      className={`rounded-lg border border-line bg-paper p-5 shadow-card ${interactiveClasses} ${className}`.trim()}
      {...rest}
    >
      {children}
    </div>
  );
}
