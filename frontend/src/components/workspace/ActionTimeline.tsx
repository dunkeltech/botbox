'use client';

import { useEffect, useState } from 'react';
import { useTranslations } from 'next-intl';

import { Button } from '@/components/ui';
import { fetchDesktopActionScreenshot } from '@/lib/api-client';
import type { DesktopAction } from '@/types/api';

export interface ActionTimelineProps {
  workspaceId: string;
  botId: string;
  actions: DesktopAction[];
  retentionDays: number;
}

/**
 * Renders the desktop action timeline for one bot.
 * Screenshot bytes stay in a short-lived object URL so the image is not fetched
 * through an unauthenticated img src and is dropped when the row closes.
 */
export function ActionTimeline({
  workspaceId,
  botId,
  actions,
  retentionDays,
}: ActionTimelineProps) {
  const t = useTranslations('workspace.computer.timeline');
  const [previewURL, setPreviewURL] = useState<string | null>(null);
  const [previewID, setPreviewID] = useState<string | null>(null);
  const [busyID, setBusyID] = useState<string | null>(null);

  useEffect(() => {
    return () => {
      if (previewURL) {
        URL.revokeObjectURL(previewURL);
      }
    };
  }, [previewURL]);

  const openScreenshot = async (actionID: string) => {
    setBusyID(actionID);
    try {
      const blob = await fetchDesktopActionScreenshot(
        workspaceId,
        botId,
        actionID,
      );
      const next = URL.createObjectURL(blob);
      setPreviewURL((current) => {
        if (current) {
          URL.revokeObjectURL(current);
        }
        return next;
      });
      setPreviewID(actionID);
    } finally {
      setBusyID(null);
    }
  };

  return (
    <div className="flex flex-col gap-3 p-4" aria-label={t('regionLabel')}>
      <p className="text-[13px] text-muted">
        {t('retention', { days: retentionDays || 7 })}
      </p>
      {actions.length === 0 ? (
        <p className="rounded-sm border border-dashed border-line bg-paper-2 px-3.5 py-2.5 text-[14px] text-ink-2">
          {t('empty')}
        </p>
      ) : (
        <ol className="flex flex-col gap-2">
          {actions.map((action) => (
            <li
              key={action.id}
              className="flex flex-wrap items-center gap-3 rounded-sm border border-line bg-paper-2 px-3.5 py-2.5"
            >
              <span className="font-mono text-[13px] font-bold text-ink">
                {action.kind}
              </span>
              {action.hasCoord &&
              action.coordX != null &&
              action.coordY != null ? (
                <span className="font-mono text-[12px] text-muted">
                  {t('coords', { x: action.coordX, y: action.coordY })}
                </span>
              ) : null}
              {action.errorCode === 'control.locked' ? (
                <span className="rounded-sm border border-amber-line bg-amber-tint px-2 py-0.5 text-[12px] font-bold text-amber">
                  {t('conflict')}
                </span>
              ) : null}
              {action.hasScreenshot ? (
                <Button
                  variant="secondary"
                  disabled={busyID === action.id}
                  onClick={() => void openScreenshot(action.id)}
                >
                  {t('screenshot')}
                </Button>
              ) : null}
              {previewID === action.id && previewURL ? (
                // The blob URL is minted in this session from an authenticated fetch.
                // eslint-disable-next-line @next/next/no-img-element
                <img
                  src={previewURL}
                  alt={t('screenshotAlt', { kind: action.kind })}
                  className="h-16 w-auto rounded-sm border border-line"
                />
              ) : null}
            </li>
          ))}
        </ol>
      )}
    </div>
  );
}
