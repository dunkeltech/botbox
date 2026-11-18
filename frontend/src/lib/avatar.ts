// SPDX-License-Identifier: AGPL-3.0-only

import { type AvatarMood } from '@/types/api';

export type AvatarHead = 'rect' | 'hex' | 'dome' | 'oct';
export type AvatarEyes = 'dual' | 'visor' | 'cyclops' | 'led';
export type AvatarAntenna = 'ball' | 'double' | 'gear';
export type AvatarMouth = 'grille' | 'dots' | 'slot';

export interface AvatarPalette {
  name: string;
  body: string;
  shade: string;
  panel: string;
  sensor: string;
  mark: string;
  accent: string;
  tile: string;
}

export interface AvatarGeometry {
  top: number;
  tipY: number;
  earL: number;
  earR: number;
  panelX: number;
  panelW: number;
}

export interface AvatarSpec {
  seed: string;
  hash: number;
  headIndex: number;
  head: AvatarHead;
  eyesIndex: number;
  eyes: AvatarEyes;
  antennaIndex: number;
  antenna: AvatarAntenna;
  mouthIndex: number;
  mouth: AvatarMouth;
  paletteIndex: number;
  palette: AvatarPalette;
  geometry: AvatarGeometry;
}

export interface AvatarMoodMotion {
  eyeVals: string;
  eyeDur: string;
  scanVals: string;
  scanDur: string;
  spinTo: number;
  spinDur: string;
  glowVals: string;
  glowDur: string;
  waves: boolean;
  dots: boolean;
}

export const AVATAR_HEADS: readonly AvatarHead[] = [
  'rect',
  'hex',
  'dome',
  'oct',
];
export const AVATAR_EYES: readonly AvatarEyes[] = [
  'dual',
  'visor',
  'cyclops',
  'led',
];
export const AVATAR_ANTENNAS: readonly AvatarAntenna[] = [
  'ball',
  'double',
  'gear',
];
export const AVATAR_MOUTHS: readonly AvatarMouth[] = ['grille', 'dots', 'slot'];

const AVATAR_TOPS = [38, 36, 30, 38] as const;

export const AVATAR_PALETTES: readonly AvatarPalette[] = [
  {
    name: 'forest',
    body: '#3f9147',
    shade: '#347a3b',
    panel: '#182019',
    sensor: '#d6fcd0',
    mark: '#182019',
    accent: '#3f9147',
    tile: '#eef8ec',
  },
  {
    name: 'night',
    body: '#2a3c2a',
    shade: '#1f2d1f',
    panel: '#14201a',
    sensor: '#5eab60',
    mark: '#5eab60',
    accent: '#3f9147',
    tile: '#d6fcd0',
  },
  {
    name: 'steel',
    body: '#c9d8c9',
    shade: '#a8bda8',
    panel: '#1f2d1f',
    sensor: '#5eab60',
    mark: '#1f2d1f',
    accent: '#3f9147',
    tile: '#f5f8f4',
  },
  {
    name: 'cobalt',
    body: '#4a5e92',
    shade: '#3f5f9c',
    panel: '#14201a',
    sensor: '#8aa8db',
    mark: '#14201a',
    accent: '#4a5e92',
    tile: '#e4e9f4',
  },
  {
    name: 'sky',
    body: '#8aa8db',
    shade: '#6d8fce',
    panel: '#1f2d1f',
    sensor: '#d6fcd0',
    mark: '#1f2d1f',
    accent: '#4a5e92',
    tile: '#edf0f6',
  },
  {
    name: 'mint',
    body: '#d6fcd0',
    shade: '#b5e4ae',
    panel: '#2a3c2a',
    sensor: '#49b24d',
    mark: '#2a3c2a',
    accent: '#3f9147',
    tile: '#e3ebe2',
  },
];

export const AVATAR_MOODS: Record<AvatarMood, AvatarMoodMotion> = {
  idle: {
    eyeVals: '1;.55;1',
    eyeDur: '3.2s',
    scanVals: '42;66;42',
    scanDur: '4.8s',
    spinTo: 360,
    spinDur: '9s',
    glowVals: '.12;.4;.12',
    glowDur: '3.2s',
    waves: false,
    dots: false,
  },
  thinking: {
    eyeVals: '1;.4;1',
    eyeDur: '1.4s',
    scanVals: '42;66;42',
    scanDur: '1.4s',
    spinTo: 360,
    spinDur: '2.2s',
    glowVals: '.2;.7;.2',
    glowDur: '1.4s',
    waves: false,
    dots: true,
  },
  action: {
    eyeVals: '1;1;.25;1;1',
    eyeDur: '1s',
    scanVals: '42;66;42',
    scanDur: '.8s',
    spinTo: 360,
    spinDur: '.9s',
    glowVals: '.5;.9;.5',
    glowDur: '.6s',
    waves: true,
    dots: false,
  },
  offline: {
    eyeVals: '.22;.22',
    eyeDur: '1s',
    scanVals: '54;54',
    scanDur: '1s',
    spinTo: 0,
    spinDur: '1s',
    glowVals: '0;0',
    glowDur: '1s',
    waves: false,
    dots: false,
  },
};

const SEED_PATTERN = /^[a-z0-9-]+$/;

/**
 * Validates that a bot avatar seed matches backend/schema constraints (^[a-z0-9-]+$, 1..100 chars).
 */
export function isValidAvatarSeed(seed: string): boolean {
  return seed.length >= 1 && seed.length <= 100 && SEED_PATTERN.test(seed);
}

/**
 * Computes unsigned 32-bit FNV-1a matching backend/internal/bots/avatar.go and Avatar.dc.html.
 */
export function fnv1a32(seed: string): number {
  let h = 2166136261 >>> 0;
  for (let i = 0; i < seed.length; i++) {
    h ^= seed.charCodeAt(i);
    h = Math.imul(h, 16777619) >>> 0;
  }
  return h >>> 0;
}

/**
 * Generates a compliant `<slug>-<4hex>` avatar seed derived from an optional bot name or slug prefix.
 */
export function generateRandomAvatarSeed(prefix = 'bot'): string {
  const cleaned = prefix
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 40);
  const base = cleaned.length > 0 ? cleaned : 'bot';

  let suffix: string;
  if (
    typeof globalThis.crypto !== 'undefined' &&
    typeof globalThis.crypto.getRandomValues === 'function'
  ) {
    const bytes = new Uint8Array(2);
    globalThis.crypto.getRandomValues(bytes);
    suffix = Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('');
  } else {
    suffix = Math.floor(Math.random() * 0xffff)
      .toString(16)
      .padStart(4, '0');
  }
  return `${base}-${suffix}`;
}

/**
 * Derives deterministic robot parts, palette, and SVG geometry for seed.
 */
export function computeAvatarSpec(seed: string): AvatarSpec {
  const effectiveSeed = seed.length > 0 ? seed : 'bot-0000';
  const hash = fnv1a32(effectiveSeed);
  const headIndex = hash % 4;
  const eyesIndex = (hash >>> 4) % 4;
  const antennaIndex = (hash >>> 8) % 3;
  const mouthIndex = (hash >>> 12) % 3;
  const paletteIndex = (hash >>> 16) % 6;

  const top = AVATAR_TOPS[headIndex] ?? 38;
  const tipY =
    antennaIndex === 0 ? top - 16 : antennaIndex === 1 ? top - 13 : top - 15;
  const isHex = headIndex === 1;

  return {
    seed: effectiveSeed,
    hash,
    headIndex,
    head: AVATAR_HEADS[headIndex] ?? 'rect',
    eyesIndex,
    eyes: AVATAR_EYES[eyesIndex] ?? 'dual',
    antennaIndex,
    antenna: AVATAR_ANTENNAS[antennaIndex] ?? 'ball',
    mouthIndex,
    mouth: AVATAR_MOUTHS[mouthIndex] ?? 'grille',
    paletteIndex,
    palette: AVATAR_PALETTES[paletteIndex] ?? AVATAR_PALETTES[0]!,
    geometry: {
      top,
      tipY,
      earL: isHex ? 16 : 18,
      earR: isHex ? 95 : 93,
      panelX: isHex ? 38 : 36,
      panelW: isHex ? 44 : 48,
    },
  };
}
