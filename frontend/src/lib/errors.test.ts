// SPDX-License-Identifier: AGPL-3.0-only

import { expect, it } from 'vitest';
import messages from '../../messages/en.json';
import { ApiClientError } from './api-client';
import { resolveErrorKey } from './errors';

it('explains a full network address pool instead of blaming the sandbox agent', () => {
  const key = resolveErrorKey(
    new ApiClientError(
      503,
      'environment.network_capacity',
      'no free network address range',
    ),
  );
  expect(key).toBe('environmentNetworkCapacity');
  expect(messages.errors[key]).toMatch(/network address range/);
});

it('explains host-wide sandbox capacity exhaustion clearly', () => {
  const key = resolveErrorKey(
    new ApiClientError(
      503,
      'environment.capacity_exceeded',
      'host sandbox capacity exceeded',
    ),
  );
  expect(key).toBe('environmentCapacityExceeded');
  expect(messages.errors[key]).toMatch(/capacity limit/);
});

it('falls back to the generic message for unknown codes and non-API failures', () => {
  expect(
    resolveErrorKey(new ApiClientError(503, 'environment.unknown_reason', 'x')),
  ).toBe('unknownError');
  expect(resolveErrorKey(new Error('environment.network_capacity'))).toBe(
    'unknownError',
  );
  expect(resolveErrorKey(undefined)).toBe('unknownError');
});
