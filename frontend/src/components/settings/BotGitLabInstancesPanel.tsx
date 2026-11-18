// SPDX-License-Identifier: AGPL-3.0-only

'use client';

import { useEffect, useState } from 'react';
import { useTranslations } from 'next-intl';
import { Alert, Button, Card } from '@/components/ui';
import { requestJson } from '@/lib/api-client';
import type { components } from '@/types/openapi.gen';

type Instance = components['schemas']['GitLabInstance'];
type InstanceList = components['schemas']['GitLabInstanceList'];

/** Assigns workspace-approved GitLab origins to one bot without changing network permissions. */
export function BotGitLabInstancesPanel({
  workspaceId,
  botId,
  canManage,
}: {
  workspaceId: string;
  botId: string;
  canManage: boolean;
}) {
  const t = useTranslations('gitlabInstances');
  const [instances, setInstances] = useState<Instance[]>([]);
  const [selected, setSelected] = useState<string[]>([]);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState(false);
  const [saved, setSaved] = useState(false);
  const path = `/api/workspaces/${workspaceId}/bots/${botId}/gitlab-instances`;
  useEffect(() => {
    let active = true;
    void (async () => {
      try {
        const [available, assigned] = await Promise.all([
          requestJson<InstanceList>(
            `/api/workspaces/${workspaceId}/gitlab-instances`,
          ),
          requestJson<InstanceList>(path),
        ]);
        if (active) {
          setInstances(available.instances);
          setSelected(assigned.instances.map((item) => item.origin));
          setLoading(false);
        }
      } catch {
        if (active) setError(true);
      }
    })();
    return () => {
      active = false;
    };
  }, [workspaceId, path]);
  async function save() {
    if (!canManage || loading || busy) return;
    setBusy(true);
    setError(false);
    setSaved(false);
    try {
      const body: components['schemas']['BotGitLabInstances'] = {
        origins: selected,
      };
      const result = await requestJson<InstanceList>(path, {
        method: 'PUT',
        body,
      });
      setSelected(result.instances.map((item) => item.origin));
      setSaved(true);
    } catch {
      setError(true);
    } finally {
      setBusy(false);
    }
  }
  return (
    <Card className="p-5">
      <h2 className="text-[20px] font-black text-ink">{t('botTitle')}</h2>
      <p className="mt-2 text-[14px] text-muted">{t('botDescription')}</p>
      {error && (
        <Alert tone="warning" className="mt-3">
          {t('error')}
        </Alert>
      )}
      {loading && !error && (
        <p role="status" className="mt-3 text-muted">
          {t('loading')}
        </p>
      )}
      {!loading && (
        <div className="my-4 grid gap-2">
          {instances.map((instance) => (
            <label
              key={instance.origin}
              className="flex min-h-[44px] items-center gap-3 rounded-sm border border-line p-3"
            >
              <input
                type="checkbox"
                className="h-5 w-5 accent-green-600 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
                checked={selected.includes(instance.origin)}
                disabled={!canManage || busy}
                onChange={(event) => {
                  setSaved(false);
                  setSelected((previous) =>
                    event.target.checked
                      ? [...previous, instance.origin]
                      : previous.filter((origin) => origin !== instance.origin),
                  );
                }}
              />
              <span>
                <span className="block font-bold text-ink">
                  {instance.name}
                </span>
                <span className="break-all font-mono text-[13px] text-ink-2">
                  {instance.origin}
                </span>
              </span>
            </label>
          ))}
          {instances.length === 0 && (
            <p className="text-muted">{t('botEmpty')}</p>
          )}
        </div>
      )}
      {canManage && (
        <Button disabled={loading || busy} onClick={() => void save()}>
          {t('saveAssignments')}
        </Button>
      )}
      {saved && (
        <p role="status" className="mt-3 font-bold text-green-600">
          {t('saved')}
        </p>
      )}
    </Card>
  );
}
