'use client';

import { type FormEvent, useState } from 'react';
import { useTranslations } from 'next-intl';

import { Alert, Button, Input } from '@/components/ui';
import { execBotTerminal } from '@/lib/api-client';
import { type ErrorTranslationKey, resolveErrorKey } from '@/lib/errors';

interface TerminalEntry {
  id: string;
  command: string;
  cwd: string;
  stdout: string;
  stderr: string;
  exitCode: number;
}

/** Runs commands and retains the transcript while another workspace tab is open. */
export function WorkspaceTerminal({
  active,
  workspaceId,
  botId,
  canManage,
  onActionError,
}: {
  active: boolean;
  workspaceId: string;
  botId: string;
  canManage: boolean;
  onActionError: (key: ErrorTranslationKey | null) => void;
}) {
  const t = useTranslations('workspace');
  const [terminalEntries, setTerminalEntries] = useState<TerminalEntry[]>([]);
  const [terminalCommand, setTerminalCommand] = useState('');
  const [terminalWorkdir, setTerminalWorkdir] = useState('/home/bot');
  const [terminalBusy, setTerminalBusy] = useState(false);

  async function handleTerminalSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!active || !canManage) {
      return;
    }
    const cmd = terminalCommand.trim();
    if (!cmd) {
      return;
    }

    onActionError(null);
    setTerminalBusy(true);
    try {
      const res = await execBotTerminal(workspaceId, botId, {
        command: cmd,
        workdir: terminalWorkdir,
      });
      setTerminalWorkdir(res.cwd);
      setTerminalEntries((prev) => [
        ...prev,
        {
          id: `${Date.now()}-${prev.length}`,
          command: cmd,
          cwd: res.cwd,
          stdout: res.stdout,
          stderr: res.stderr,
          exitCode: res.exitCode,
        },
      ]);
      setTerminalCommand('');
    } catch (err) {
      onActionError(resolveErrorKey(err));
    } finally {
      setTerminalBusy(false);
    }
  }

  if (!active) return null;

  return (
    <div className="flex flex-col">
      <div className="min-h-[320px] max-h-[420px] overflow-auto bg-terminal p-4 font-mono text-[13px] leading-[1.7] text-on-dark">
        <p className="text-on-dark-muted">
          {t('terminal.header', { cwd: terminalWorkdir })}
        </p>
        {terminalEntries.length === 0 ? (
          <p className="mt-2 text-on-dark-muted">{t('terminal.emptyHint')}</p>
        ) : (
          <div className="mt-3 flex flex-col gap-3">
            {terminalEntries.map((entry) => (
              <div key={entry.id} className="flex flex-col gap-1">
                <div className="flex items-center justify-between gap-2">
                  <span className="font-bold text-green-light">
                    {`$ ${entry.command}`}
                  </span>
                  <span
                    className={`text-[12px] font-bold ${
                      entry.exitCode === 0
                        ? 'text-green-light'
                        : 'text-amber-on-dark'
                    }`}
                  >
                    {t('terminal.exitCodeLabel', {
                      code: entry.exitCode,
                    })}
                  </span>
                </div>
                {entry.stdout ? (
                  <pre className="whitespace-pre-wrap break-words text-on-dark">
                    {entry.stdout}
                  </pre>
                ) : null}
                {entry.stderr ? (
                  <pre className="whitespace-pre-wrap break-words text-amber-on-dark">
                    {entry.stderr}
                  </pre>
                ) : null}
                {!entry.stdout && !entry.stderr ? (
                  <span className="text-on-dark-muted">
                    {t('terminal.noOutput')}
                  </span>
                ) : null}
              </div>
            ))}
          </div>
        )}
      </div>

      <form
        onSubmit={(e) => void handleTerminalSubmit(e)}
        className="flex flex-col gap-3 border-t border-line bg-paper p-4"
      >
        {!canManage ? (
          <Alert tone="info">{t('terminal.viewerNotice')}</Alert>
        ) : null}
        <div className="flex flex-wrap items-end gap-3">
          <div className="min-w-[220px] flex-1">
            <Input
              label={t('terminal.commandLabel')}
              mono
              value={terminalCommand}
              readOnly={!canManage}
              onChange={(e) => setTerminalCommand(e.target.value)}
              placeholder={t('terminal.commandPlaceholder')}
            />
          </div>
          <Button
            type="submit"
            disabled={!canManage || terminalBusy || !terminalCommand.trim()}
          >
            {terminalBusy
              ? t('terminal.runningButton')
              : t('terminal.runButton')}
          </Button>
        </div>
      </form>
    </div>
  );
}
