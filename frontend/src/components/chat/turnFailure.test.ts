import { expect, it } from 'vitest';
import { turnFailureKey } from './turnFailure';

it('uses public recovery copy for failures and never returns unknown dependency text', () => {
  expect(turnFailureKey('engine.provider_failed')).toBe('providerFailed');
  expect(turnFailureKey('engine.interrupted')).toBe('turnInterrupted');
  expect(turnFailureKey('budget_exceeded')).toBe('budgetExceeded');
  for (const code of [
    '',
    'constructor',
    'private fixture credential from provider',
  ]) {
    expect(turnFailureKey(code)).toBe('turnFailed');
  }
});
