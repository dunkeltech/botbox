// SPDX-License-Identifier: AGPL-3.0-only

'use client';

import { FileText, Folder, RefreshCw } from 'lucide-react';
import { type FormEvent, useCallback, useEffect, useState } from 'react';
import { useTranslations } from 'next-intl';

import { Alert, Button, Input, Textarea } from '@/components/ui';
import {
  ApiClientError,
  listBotFiles,
  readBotFile,
  writeBotFile,
} from '@/lib/api-client';
import { type ErrorTranslationKey, resolveErrorKey } from '@/lib/errors';
import type { FileEntry } from '@/types/api';

function parentHomePath(path: string): string {
  if (!path || path === '/home/bot') {
    return '/home/bot';
  }
  const trimmed = path.replace(/\/+$/, '');
  const idx = trimmed.lastIndexOf('/');
  if (idx <= '/home/bot'.length) {
    return '/home/bot';
  }
  return trimmed.slice(0, idx) || '/home/bot';
}

/** Browses and edits workspace files, retaining the editor draft across tabs. */
export function WorkspaceFiles({
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
  const [filesPath, setFilesPath] = useState('/home/bot');
  const [fileEntries, setFileEntries] = useState<FileEntry[]>([]);
  const [filesLoading, setFilesLoading] = useState(false);
  const [pathInput, setPathInput] = useState('/home/bot/notes.txt');
  const [selectedFilePath, setSelectedFilePath] = useState<string | null>(null);
  const [editorContent, setEditorContent] = useState('');
  const [fileBusy, setFileBusy] = useState<'read' | 'write' | null>(null);
  const [fileSavedPath, setFileSavedPath] = useState<string | null>(null);

  const loadDirectory = useCallback(
    async (targetPath: string) => {
      setFilesLoading(true);
      onActionError(null);
      try {
        const res = await listBotFiles(workspaceId, botId, targetPath);
        setFilesPath(res.path);
        setFileEntries(res.entries);
      } catch (err) {
        onActionError(resolveErrorKey(err));
      } finally {
        setFilesLoading(false);
      }
    },
    [workspaceId, botId, onActionError],
  );

  useEffect(() => {
    if (active) {
      void loadDirectory(filesPath);
    }
  }, [active, filesPath, loadDirectory]);

  async function handleOpenFile(targetPath: string) {
    if (!active) {
      return;
    }
    const normalized = targetPath.trim();
    if (!normalized) {
      return;
    }
    onActionError(null);
    setFileSavedPath(null);
    setFileBusy('read');
    try {
      const res = await readBotFile(workspaceId, botId, normalized);
      setSelectedFilePath(res.path);
      setPathInput(res.path);
      setEditorContent(res.content);
    } catch (err) {
      if (err instanceof ApiClientError && err.code === 'file.not_found') {
        setSelectedFilePath(normalized);
        setPathInput(normalized);
        setEditorContent('');
      } else {
        onActionError(resolveErrorKey(err));
      }
    } finally {
      setFileBusy(null);
    }
  }

  async function handleOpenPathForm(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    await handleOpenFile(pathInput);
  }

  async function handleSaveFile() {
    if (!active || !canManage || !selectedFilePath) {
      return;
    }
    onActionError(null);
    setFileSavedPath(null);
    setFileBusy('write');
    try {
      const saved = await writeBotFile(workspaceId, botId, {
        path: selectedFilePath,
        content: editorContent,
      });
      setSelectedFilePath(saved.path);
      setEditorContent(saved.content);
      setFileSavedPath(saved.path);
      await loadDirectory(filesPath);
    } catch (err) {
      onActionError(resolveErrorKey(err));
    } finally {
      setFileBusy(null);
    }
  }

  if (!active) return null;

  return (
    <div className="flex flex-col gap-4 p-4">
      {!canManage ? (
        <Alert tone="info">{t('files.viewerReadonly')}</Alert>
      ) : null}

      {fileSavedPath ? (
        <Alert tone="success">
          {t('files.savedFile', { path: fileSavedPath })}
        </Alert>
      ) : null}

      <div className="flex flex-wrap items-center justify-between gap-2 border-b border-line pb-3">
        <p className="font-mono text-[13px] font-bold text-ink-2">
          {t('files.currentPath', { path: filesPath })}
        </p>
        <div className="flex flex-wrap gap-2">
          <Button
            variant="secondary"
            disabled={filesPath === '/home/bot' || filesLoading}
            onClick={() => setFilesPath(parentHomePath(filesPath))}
          >
            {t('files.upOneLevel')}
          </Button>
          <Button
            variant="secondary"
            disabled={filesLoading}
            onClick={() => void loadDirectory(filesPath)}
          >
            <RefreshCw className="h-4 w-4" aria-hidden="true" />
            <span>{t('files.refresh')}</span>
          </Button>
        </div>
      </div>

      <form
        onSubmit={(e) => void handleOpenPathForm(e)}
        className="flex flex-wrap items-end gap-3"
      >
        <div className="min-w-[220px] flex-1">
          <Input
            label={t('files.newFileLabel')}
            mono
            value={pathInput}
            onChange={(e) => setPathInput(e.target.value)}
            placeholder={t('files.newFilePlaceholder')}
          />
        </div>
        <Button
          type="submit"
          variant="secondary"
          disabled={fileBusy !== null || !pathInput.trim()}
        >
          {t('files.openPathButton')}
        </Button>
      </form>

      <div className="grid gap-4 lg:grid-cols-[260px_1fr]">
        <div className="flex flex-col divide-y divide-line overflow-hidden rounded-sm border border-line bg-paper-2">
          {fileEntries.length === 0 ? (
            <p className="p-4 text-[13px] text-muted">{t('files.emptyDir')}</p>
          ) : (
            fileEntries.map((entry) => (
              <button
                key={entry.path}
                type="button"
                onClick={() => {
                  if (entry.isDir) {
                    setFilesPath(entry.path);
                  } else {
                    void handleOpenFile(entry.path);
                  }
                }}
                className={`flex min-h-[44px] items-center justify-between gap-2 px-3 py-2 text-left text-[13px] transition duration-150 hover:bg-mint-soft focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-[-2px] focus-visible:outline-green ${
                  selectedFilePath === entry.path
                    ? 'bg-mint-soft font-bold text-green-600'
                    : 'text-ink'
                }`}
              >
                <span className="flex min-w-0 items-center gap-2 font-mono">
                  {entry.isDir ? (
                    <Folder
                      className="h-4 w-4 shrink-0 text-green-600"
                      aria-hidden="true"
                    />
                  ) : (
                    <FileText
                      className="h-4 w-4 shrink-0 text-muted"
                      aria-hidden="true"
                    />
                  )}
                  <span className="truncate">{entry.name}</span>
                </span>
                <span className="shrink-0 font-mono text-[11px] text-muted">
                  {entry.isDir
                    ? t('files.dirBadge')
                    : t('files.bytesLabel', {
                        bytes: entry.sizeBytes,
                      })}
                </span>
              </button>
            ))
          )}
        </div>

        <div className="flex flex-col gap-3">
          <p className="font-mono text-[13px] font-bold text-ink">
            {selectedFilePath
              ? t('files.editorTitle', { path: selectedFilePath })
              : t('files.editorSelectPrompt')}
          </p>
          <Textarea
            label={t('files.editorContentLabel')}
            value={editorContent}
            readOnly={!canManage || !selectedFilePath}
            rows={10}
            className="font-mono text-[13px]"
            onChange={(e) => setEditorContent(e.target.value)}
          />
          <div className="flex justify-end">
            <Button
              disabled={!canManage || !selectedFilePath || fileBusy !== null}
              onClick={() => void handleSaveFile()}
            >
              {fileBusy === 'write'
                ? t('files.savingFile')
                : t('files.saveFile')}
            </Button>
          </div>
        </div>
      </div>
    </div>
  );
}
