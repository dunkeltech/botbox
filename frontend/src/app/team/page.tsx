'use client';

import { useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { useTranslations } from 'next-intl';
import { AppShell } from '@/components/shell/AppShell';
import { ChatPanel } from '@/components/chat/ChatPanel';
import { Button, Card } from '@/components/ui';
import { useAuth } from '@/context/AuthContext';
import { listBots } from '@/lib/api-client';

function TeamContent() {
  const t = useTranslations('chat');
  const { activeWorkspace, user } = useAuth();
  const [selected, setSelected] = useState<string | null>(null);
  const bots = useQuery({
    queryKey: ['team-bots', activeWorkspace?.id],
    enabled: !!activeWorkspace,
    queryFn: () => listBots(activeWorkspace!.id),
  });
  if (!activeWorkspace) return null;
  const bot =
    bots.data?.bots.find((bot) => bot.id === selected) ?? bots.data?.bots[0];
  return (
    <div className="grid gap-5 lg:grid-cols-[240px_1fr]">
      <Card className="flex flex-col gap-3 p-4">
        <h1 className="text-[20px] font-black">{t('team')}</h1>
        {bots.data?.bots.map((bot) => (
          <Button
            key={bot.id}
            variant="secondary"
            onClick={() => setSelected(bot.id)}
          >
            {bot.name}
          </Button>
        ))}
      </Card>
      <Card className="p-5">
        {bot ? (
          <ChatPanel
            key={`${activeWorkspace.id}/${user?.id}/${bot.id}`}
            workspaceId={activeWorkspace.id}
            userId={user?.id ?? ''}
            botId={bot.id}
            name={bot.name}
            avatarSeed={bot.avatarSeed}
            canChat={activeWorkspace.role !== 'viewer'}
          />
        ) : (
          <p>{t('empty')}</p>
        )}
      </Card>
    </div>
  );
}
/** Team direct-message view with a workspace-scoped bot list. */
export default function TeamPage() {
  return (
    <AppShell>
      <TeamContent />
    </AppShell>
  );
}
