'use client';

import { type FormEvent, useEffect, useState } from 'react';
import { useTranslations } from 'next-intl';
import { Alert, Button, Card, Input } from '@/components/ui';
import { requestJson } from '@/lib/api-client';
import type { components } from '@/types/openapi.gen';
import type { WorkspaceRole } from '@/types/api';

type Instance = components['schemas']['GitLabInstance'];
type InstanceList = components['schemas']['GitLabInstanceList'];

/** Manages the current workspace's approved GitLab browser login destinations. */
export function GitLabInstancesPanel({
  workspaceId,
  role,
}: {
  workspaceId: string;
  role: WorkspaceRole;
}) {
  const t = useTranslations('gitlabInstances');
  const [instances, setInstances] = useState<Instance[]>([]);
  const [name, setName] = useState('');
  const [origin, setOrigin] = useState('');
  const [busy, setBusy] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState(false);
  const canManage = role === 'owner' || role === 'admin';
  const path = `/api/workspaces/${workspaceId}/gitlab-instances`;

  useEffect(() => {
    let active = true;
    requestJson<InstanceList>(path)
      .then((result) => {
        if (active) setInstances(result.instances);
      })
      .catch(() => {
        if (active) setError(true);
      })
      .finally(() => {
        if (active) setLoading(false);
      });
    return () => {
      active = false;
    };
  }, [path]);

  async function add(event: FormEvent) {
    event.preventDefault();
    if (!canManage || busy) return;
    setBusy(true);
    setError(false);
    try {
      const instance = await requestJson<Instance>(path, {
        method: 'POST',
        body: { name, origin },
      });
      setInstances((previous) => [...previous, instance]);
      setName('');
      setOrigin('');
    } catch {
      setError(true);
    } finally {
      setBusy(false);
    }
  }

  async function remove(instance: Instance) {
    if (!canManage || busy) return;
    setBusy(true);
    setError(false);
    try {
      await requestJson<void>(path, {
        method: 'DELETE',
        body: { origin: instance.origin },
      });
      setInstances((previous) =>
        previous.filter((item) => item.origin !== instance.origin),
      );
    } catch {
      setError(true);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card className="p-5">
      <h2 className="text-[20px] font-black text-ink">{t('title')}</h2>
      <p className="mt-2 text-[14px] text-muted">{t('description')}</p>
      {error && <Alert className="mt-3">{t('error')}</Alert>}
      {loading ? (
        <p role="status" className="mt-3 text-muted">
          {t('loading')}
        </p>
      ) : (
        <ul className="my-4 space-y-3">
          {instances.map((instance) => (
            <li
              key={instance.origin}
              className="flex flex-wrap items-center justify-between gap-3 border-b border-line pb-3"
            >
              <div>
                <p className="font-bold text-ink">{instance.name}</p>
                <p className="break-all font-mono text-[13px] text-ink-2">
                  {instance.origin}
                </p>
              </div>
              {canManage && (
                <Button
                  variant="secondary"
                  disabled={busy}
                  onClick={() => void remove(instance)}
                  aria-label={t('removeNamed', { name: instance.name })}
                >
                  {t('remove')}
                </Button>
              )}
            </li>
          ))}
          {instances.length === 0 && (
            <li className="text-muted">{t('empty')}</li>
          )}
        </ul>
      )}
      {canManage && (
        <form onSubmit={(event) => void add(event)} className="mt-4 grid gap-3">
          <Input
            label={t('name')}
            value={name}
            onChange={(event) => setName(event.target.value)}
            required
            maxLength={100}
            disabled={busy}
          />
          <Input
            label={t('origin')}
            type="url"
            value={origin}
            onChange={(event) => setOrigin(event.target.value)}
            required
            maxLength={512}
            mono
            disabled={busy}
            helperText={t('originHint')}
          />
          <div>
            <Button
              type="submit"
              disabled={busy || loading || instances.length >= 20}
            >
              {t('add')}
            </Button>
          </div>
        </form>
      )}
      <p className="mt-3 text-[14px] text-muted">{t('revocation')}</p>
    </Card>
  );
}
