import { expect, it } from 'vitest';
import {
  PHASE_DEVELOPMENT_SERVER,
  PHASE_PRODUCTION_BUILD,
  PHASE_PRODUCTION_SERVER,
} from 'next/constants.js';
import configureNext from '../../next.config.mjs';

it('keeps dev output separate while production build and start share output', () => {
  const development = configureNext(PHASE_DEVELOPMENT_SERVER);
  const build = configureNext(PHASE_PRODUCTION_BUILD);
  const serve = configureNext(PHASE_PRODUCTION_SERVER);
  expect(development.distDir).not.toBe(build.distDir);
  expect(serve.distDir).toBe(build.distDir);
});
