'use client';

import { useTranslations } from 'next-intl';

import { AVATAR_MOODS, computeAvatarSpec } from '@/lib/avatar';
import { type AvatarMood } from '@/types/api';

export interface BotAvatarProps {
  seed: string;
  size?: number;
  mood?: AvatarMood;
  tile?: boolean;
  showStatusDot?: boolean;
  decorative?: boolean;
  ariaLabel?: string;
  className?: string;
}

const DOT_COLOR_BY_MOOD: Record<AvatarMood, string> = {
  idle: 'bg-on-dark-faint',
  thinking: 'bg-blue',
  action: 'bg-green',
  offline: 'bg-line-strong',
};

/**
 * Deterministic robot SVG avatar matching design/source/Avatar.dc.html and STYLEGUIDE.md §8.
 */
export function BotAvatar({
  seed,
  size = 64,
  mood = 'idle',
  tile = true,
  showStatusDot = false,
  decorative = false,
  ariaLabel,
  className = '',
}: BotAvatarProps) {
  const t = useTranslations('avatar');
  const spec = computeAvatarSpec(seed);
  const motion = AVATAR_MOODS[mood] ?? AVATAR_MOODS.idle;
  const { palette: p, geometry: g } = spec;

  const topIn = g.top + 2;
  const stemEnd = g.top - 11;
  const dblEnd = g.top - 10;
  const dblTip = g.top - 13;
  const gearStem = g.top - 7;
  const gearY = g.top - 15;

  const resolvedLabel = ariaLabel ?? t('ariaLabel', { seed: spec.seed, mood });

  const svg = (
    <svg
      width={size}
      height={size}
      viewBox="0 0 120 120"
      role={decorative ? undefined : 'img'}
      aria-label={decorative ? undefined : resolvedLabel}
      aria-hidden={decorative ? true : undefined}
      className={showStatusDot ? 'block' : `block shrink-0 ${className}`.trim()}
    >
      <rect
        x="0"
        y="0"
        width="120"
        height="120"
        rx="26"
        fill={tile ? p.tile : 'none'}
      />

      {motion.waves ? (
        <g data-avatar-part="waves">
          <circle
            cx="60"
            cy={g.tipY}
            r="6"
            fill="none"
            stroke={p.accent}
            strokeWidth="2.5"
            opacity="0"
          >
            <animate
              attributeName="r"
              values="6;24"
              dur="1.4s"
              repeatCount="indefinite"
            />
            <animate
              attributeName="opacity"
              values=".8;0"
              dur="1.4s"
              repeatCount="indefinite"
            />
          </circle>
          <circle
            cx="60"
            cy={g.tipY}
            r="6"
            fill="none"
            stroke={p.accent}
            strokeWidth="2.5"
            opacity="0"
          >
            <animate
              attributeName="r"
              values="6;24"
              dur="1.4s"
              begin=".7s"
              repeatCount="indefinite"
            />
            <animate
              attributeName="opacity"
              values=".8;0"
              dur="1.4s"
              begin=".7s"
              repeatCount="indefinite"
            />
          </circle>
        </g>
      ) : null}

      {spec.antenna === 'ball' ? (
        <g data-avatar-part="antenna-ball">
          <line
            x1="60"
            y1={g.top}
            x2="60"
            y2={stemEnd}
            stroke="#182019"
            strokeWidth="3"
            strokeLinecap="round"
          />
          <circle cx="60" cy={g.tipY} r="9" fill={p.accent} opacity=".2">
            <animate
              attributeName="opacity"
              values={motion.glowVals}
              dur={motion.glowDur}
              repeatCount="indefinite"
            />
          </circle>
          <circle
            cx="60"
            cy={g.tipY}
            r="5"
            fill={p.sensor}
            stroke="#182019"
            strokeWidth="2.5"
          />
        </g>
      ) : null}

      {spec.antenna === 'double' ? (
        <g data-avatar-part="antenna-double">
          <line
            x1="50"
            y1={topIn}
            x2="45"
            y2={dblEnd}
            stroke="#182019"
            strokeWidth="3"
            strokeLinecap="round"
          />
          <line
            x1="70"
            y1={topIn}
            x2="75"
            y2={dblEnd}
            stroke="#182019"
            strokeWidth="3"
            strokeLinecap="round"
          />
          <circle
            cx="45"
            cy={dblTip}
            r="4"
            fill={p.sensor}
            stroke="#182019"
            strokeWidth="2.5"
          >
            <animate
              attributeName="opacity"
              values={motion.eyeVals}
              dur={motion.glowDur}
              repeatCount="indefinite"
            />
          </circle>
          <circle
            cx="75"
            cy={dblTip}
            r="4"
            fill={p.sensor}
            stroke="#182019"
            strokeWidth="2.5"
          >
            <animate
              attributeName="opacity"
              values={motion.eyeVals}
              dur={motion.glowDur}
              begin=".3s"
              repeatCount="indefinite"
            />
          </circle>
        </g>
      ) : null}

      {spec.antenna === 'gear' ? (
        <g data-avatar-part="antenna-gear">
          <line
            x1="60"
            y1={g.top}
            x2="60"
            y2={gearStem}
            stroke="#182019"
            strokeWidth="3"
            strokeLinecap="round"
          />
          <g transform={`translate(60 ${gearY})`}>
            <g>
              <animateTransform
                attributeName="transform"
                type="rotate"
                from="0"
                to={motion.spinTo}
                dur={motion.spinDur}
                repeatCount="indefinite"
              />
              {[0, 60, 120, 180, 240, 300].map((deg) => (
                <rect
                  key={deg}
                  x="-2.5"
                  y="-10.5"
                  width="5"
                  height="6"
                  rx="1"
                  fill={p.shade}
                  stroke="#182019"
                  strokeWidth="2"
                  transform={deg === 0 ? undefined : `rotate(${deg})`}
                />
              ))}
              <circle
                cx="0"
                cy="0"
                r="7"
                fill={p.body}
                stroke="#182019"
                strokeWidth="2.5"
              />
              <circle cx="0" cy="0" r="2.5" fill={p.panel} />
            </g>
          </g>
        </g>
      ) : null}

      <rect
        x="50"
        y="90"
        width="20"
        height="12"
        rx="2"
        fill={p.shade}
        stroke="#182019"
        strokeWidth="3"
      />
      <rect
        x="38"
        y="100"
        width="44"
        height="8"
        rx="4"
        fill={p.body}
        stroke="#182019"
        strokeWidth="3"
      />
      <rect
        x={g.earL}
        y="56"
        width="9"
        height="20"
        rx="3"
        fill={p.shade}
        stroke="#182019"
        strokeWidth="3"
      />
      <rect
        x={g.earR}
        y="56"
        width="9"
        height="20"
        rx="3"
        fill={p.shade}
        stroke="#182019"
        strokeWidth="3"
      />

      {spec.head === 'rect' ? (
        <rect
          x="26"
          y="38"
          width="68"
          height="56"
          rx="14"
          fill={p.body}
          stroke="#182019"
          strokeWidth="3"
        />
      ) : null}
      {spec.head === 'hex' ? (
        <polygon
          points="42,36 78,36 96,66 78,96 42,96 24,66"
          fill={p.body}
          stroke="#182019"
          strokeWidth="3"
          strokeLinejoin="round"
        />
      ) : null}
      {spec.head === 'dome' ? (
        <path
          d="M26 92 V64 A34 34 0 0 1 94 64 V92 Q94 94 92 94 H28 Q26 94 26 92 Z"
          fill={p.body}
          stroke="#182019"
          strokeWidth="3"
          strokeLinejoin="round"
        />
      ) : null}
      {spec.head === 'oct' ? (
        <polygon
          points="37,38 83,38 94,49 94,83 83,94 37,94 26,83 26,49"
          fill={p.body}
          stroke="#182019"
          strokeWidth="3"
          strokeLinejoin="round"
        />
      ) : null}

      <path
        d="M40 45 H52"
        stroke="#ffffff"
        strokeOpacity=".45"
        strokeWidth="3"
        strokeLinecap="round"
      />
      <rect
        x={g.panelX}
        y="50"
        width={g.panelW}
        height="30"
        rx="8"
        fill={p.panel}
      />

      {spec.eyes === 'dual' ? (
        <g data-avatar-part="eyes-dual">
          <g>
            <animate
              attributeName="opacity"
              values={motion.eyeVals}
              dur={motion.eyeDur}
              repeatCount="indefinite"
            />
            <circle cx="48" cy="65" r="6.5" fill={p.sensor} />
            <circle cx="72" cy="65" r="6.5" fill={p.sensor} />
          </g>
          <circle cx="46" cy="63" r="2" fill="#ffffff" opacity=".75" />
          <circle cx="70" cy="63" r="2" fill="#ffffff" opacity=".75" />
        </g>
      ) : null}

      {spec.eyes === 'visor' ? (
        <g data-avatar-part="eyes-visor">
          <rect
            x="42"
            y="60"
            width="36"
            height="10"
            rx="5"
            fill={p.sensor}
            opacity=".3"
          />
          <rect x="42" y="60" width="12" height="10" rx="5" fill={p.sensor}>
            <animate
              attributeName="x"
              values={motion.scanVals}
              dur={motion.scanDur}
              repeatCount="indefinite"
            />
            <animate
              attributeName="opacity"
              values={motion.eyeVals}
              dur={motion.eyeDur}
              repeatCount="indefinite"
            />
          </rect>
        </g>
      ) : null}

      {spec.eyes === 'cyclops' ? (
        <g data-avatar-part="eyes-cyclops">
          <circle
            cx="60"
            cy="65"
            r="11"
            fill="none"
            stroke={p.sensor}
            strokeWidth="2"
            opacity=".45"
          />
          <circle cx="60" cy="65" r="7" fill={p.sensor}>
            <animate
              attributeName="opacity"
              values={motion.eyeVals}
              dur={motion.eyeDur}
              repeatCount="indefinite"
            />
          </circle>
          <circle cx="60" cy="65" r="2.5" fill={p.panel} />
        </g>
      ) : null}

      {spec.eyes === 'led' ? (
        <g data-avatar-part="eyes-led">
          <g fill={p.sensor}>
            <animate
              attributeName="opacity"
              values={motion.eyeVals}
              dur={motion.eyeDur}
              repeatCount="indefinite"
            />
            <rect x="43" y="58" width="5" height="5" rx="1" />
            <rect x="50" y="58" width="5" height="5" rx="1" />
            <rect x="43" y="65" width="5" height="5" rx="1" />
            <rect x="50" y="65" width="5" height="5" rx="1" opacity=".5" />
          </g>
          <g fill={p.sensor}>
            <animate
              attributeName="opacity"
              values={motion.eyeVals}
              dur={motion.eyeDur}
              begin=".4s"
              repeatCount="indefinite"
            />
            <rect x="65" y="58" width="5" height="5" rx="1" />
            <rect x="72" y="58" width="5" height="5" rx="1" />
            <rect x="65" y="65" width="5" height="5" rx="1" opacity=".5" />
            <rect x="72" y="65" width="5" height="5" rx="1" />
          </g>
        </g>
      ) : null}

      {spec.mouth === 'grille' ? (
        <g stroke={p.mark} strokeWidth="2.5" strokeLinecap="round">
          <line x1="51" y1="84" x2="51" y2="89" />
          <line x1="57" y1="84" x2="57" y2="89" />
          <line x1="63" y1="84" x2="63" y2="89" />
          <line x1="69" y1="84" x2="69" y2="89" />
        </g>
      ) : null}
      {spec.mouth === 'dots' ? (
        <g fill={p.mark}>
          <circle cx="52" cy="87" r="2" />
          <circle cx="60" cy="87" r="2" />
          <circle cx="68" cy="87" r="2" />
        </g>
      ) : null}
      {spec.mouth === 'slot' ? (
        <rect x="47" y="85" width="26" height="4" rx="2" fill={p.mark} />
      ) : null}

      {motion.dots ? (
        <g data-avatar-part="thinking-dots">
          <rect
            x="82"
            y="10"
            width="30"
            height="15"
            rx="7.5"
            fill="#ffffff"
            stroke="#182019"
            strokeWidth="2"
          />
          <circle cx="90" cy="17.5" r="2.2" fill="#182019">
            <animate
              attributeName="opacity"
              values=".25;1;.25"
              dur="1.2s"
              repeatCount="indefinite"
            />
          </circle>
          <circle cx="97" cy="17.5" r="2.2" fill="#182019">
            <animate
              attributeName="opacity"
              values=".25;1;.25"
              dur="1.2s"
              begin=".2s"
              repeatCount="indefinite"
            />
          </circle>
          <circle cx="104" cy="17.5" r="2.2" fill="#182019">
            <animate
              attributeName="opacity"
              values=".25;1;.25"
              dur="1.2s"
              begin=".4s"
              repeatCount="indefinite"
            />
          </circle>
        </g>
      ) : null}
    </svg>
  );

  if (!showStatusDot) {
    return svg;
  }

  return (
    <span className={`relative inline-block shrink-0 ${className}`.trim()}>
      {svg}
      <span
        aria-hidden="true"
        className={`absolute -bottom-0.5 -right-0.5 h-3 w-3 rounded-pill ring-2 ring-paper ${DOT_COLOR_BY_MOOD[mood]}`}
      />
    </span>
  );
}
