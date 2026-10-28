'use client';

import { Check, Copy } from 'lucide-react';
import {
  createContext,
  isValidElement,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ComponentPropsWithoutRef,
  type ReactNode,
} from 'react';
import ReactMarkdown, { type Components } from 'react-markdown';
import remarkGfm from 'remark-gfm';
import { useTranslations } from 'next-intl';
import { remarkChatText } from '@/components/chat/remarkChatText';

export type MarkdownTone = 'light' | 'dark';

const InsideLinkContext = createContext(false);

// Track the last announced copy status so repeated actions remain distinct for
// screen readers that suppress identical live-region updates.
let lastCopyAnnouncement = '';

function nextAnnouncement(message: string): string {
  const value = message === lastCopyAnnouncement ? `${message}\u00A0` : message;
  lastCopyAnnouncement = message;
  return value;
}

/**
 * Validates Markdown link and image destinations against an explicit protocol
 * allowlist so scriptable or relative paths cannot become clickable targets.
 */
export function safeChatUrl(
  raw: string | undefined,
  allowMailto = true,
): string | null {
  if (!raw) return null;
  const trimmed = raw.trim();
  if (!trimmed) return null;
  for (let i = 0; i < trimmed.length; i += 1) {
    const code = trimmed.charCodeAt(i);
    if (code <= 0x1f || code === 0x7f) return null;
  }
  try {
    const parsed = new URL(trimmed);
    if (parsed.protocol === 'http:' || parsed.protocol === 'https:') {
      return trimmed;
    }
    if (allowMailto && parsed.protocol === 'mailto:') {
      return trimmed;
    }
    return null;
  } catch {
    return null;
  }
}

function extractText(node: ReactNode): string {
  if (typeof node === 'string' || typeof node === 'number') {
    return String(node);
  }
  if (Array.isArray(node)) {
    return node.map(extractText).join('');
  }
  if (isValidElement<{ children?: ReactNode }>(node)) {
    return extractText(node.props.children);
  }
  return '';
}

function extractCodeBlock(children: ReactNode): {
  code: string;
  language: string;
} {
  if (isValidElement<{ className?: string; children?: ReactNode }>(children)) {
    const match = /language-([^\s]+)/.exec(children.props.className ?? '');
    return {
      code: extractText(children.props.children).replace(/\n$/, ''),
      language: match?.[1] ?? '',
    };
  }
  return {
    code: extractText(children).replace(/\n$/, ''),
    language: '',
  };
}

function CodeBlock({
  code,
  language,
  tone,
}: {
  code: string;
  language: string;
  tone: MarkdownTone;
}) {
  const t = useTranslations('chat');
  const [status, setStatus] = useState<'idle' | 'copied' | 'failed'>('idle');
  const [announcement, setAnnouncement] = useState('');
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => {
    return () => {
      if (timerRef.current) clearTimeout(timerRef.current);
    };
  }, []);

  async function handleCopy() {
    if (timerRef.current) clearTimeout(timerRef.current);
    let ok = false;
    try {
      if (!navigator.clipboard?.writeText) {
        throw new Error('Clipboard unavailable');
      }
      await navigator.clipboard.writeText(code);
      ok = true;
    } catch {
      ok = false;
    }
    setStatus(ok ? 'copied' : 'failed');
    setAnnouncement(
      nextAnnouncement(ok ? t('copiedCode') : t('copyCodeFailed')),
    );
    timerRef.current = setTimeout(() => {
      setStatus('idle');
      setAnnouncement('');
    }, 2000);
  }

  return (
    <div
      className={`my-2 overflow-hidden rounded-sm border bg-terminal text-on-dark ${
        tone === 'dark' ? 'border-on-dark/25' : 'border-dark'
      }`}
    >
      <div className="flex items-center justify-between gap-2 border-b border-dark bg-dark-2 px-3">
        <span className="truncate font-mono text-[11px] font-bold uppercase tracking-[0.06em] text-on-dark-muted">
          {language || t('codeBlock')}
        </span>
        <div className="flex items-center gap-2">
          <span
            role="status"
            className={`text-[12px] font-bold ${
              status === 'failed' ? 'text-amber-on-dark' : 'text-green-light'
            }`}
          >
            {announcement}
          </span>
          <button
            type="button"
            aria-label={t('copyCode')}
            onClick={() => void handleCopy()}
            className="inline-flex min-h-[44px] min-w-[44px] items-center justify-center rounded-xs text-on-dark hover:bg-dark focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
          >
            {status === 'copied' ? (
              <Check className="h-4 w-4 text-green-light" aria-hidden="true" />
            ) : (
              <Copy className="h-4 w-4" aria-hidden="true" />
            )}
          </button>
        </div>
      </div>
      <pre
        tabIndex={0}
        className="overflow-x-auto px-3.5 py-2.5 font-mono text-[13px] leading-[1.55] text-on-dark focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green"
      >
        <code>{code}</code>
      </pre>
    </div>
  );
}

/**
 * Safely renders chat messages as CommonMark + GFM without allowing raw HTML,
 * unsafe link schemes, or unauthenticated external image loads.
 */
export function MarkdownMessage({
  content,
  tone = 'light',
  mentionNames = [],
  className = '',
}: {
  content: string;
  tone?: MarkdownTone;
  mentionNames?: string[];
  className?: string;
}) {
  const t = useTranslations('chat');
  const isDark = tone === 'dark';
  const mentionKey = mentionNames.join('\0');

  const remarkPlugins = useMemo<
    NonNullable<ComponentPropsWithoutRef<typeof ReactMarkdown>['remarkPlugins']>
  >(
    () => [remarkGfm, [remarkChatText, mentionNames]],
    // eslint-disable-next-line react-hooks/exhaustive-deps -- keyed on content so inline arrays do not re-parse every keystroke
    [mentionKey],
  );

  const components = useMemo<Components>(
    () => ({
      p: ({ children }) => <p className="my-1.5 break-words">{children}</p>,
      h1: ({ children }) => (
        <h1 className="mb-1 mt-3 text-[1.15em] font-black leading-snug tracking-[-0.01em]">
          {children}
        </h1>
      ),
      h2: ({ children }) => (
        <h2 className="mb-1 mt-2.5 text-[1.08em] font-black leading-snug tracking-[-0.01em]">
          {children}
        </h2>
      ),
      h3: ({ children }) => (
        <h3 className="mb-1 mt-2 text-[1.02em] font-bold leading-snug">
          {children}
        </h3>
      ),
      h4: ({ children }) => (
        <h4 className="mb-1 mt-2 text-[1em] font-bold leading-snug">
          {children}
        </h4>
      ),
      h5: ({ children }) => (
        <h5 className="mb-1 mt-1.5 text-[0.95em] font-bold leading-snug">
          {children}
        </h5>
      ),
      h6: ({ children }) => (
        <h6 className="mb-1 mt-1.5 text-[0.9em] font-bold uppercase tracking-[0.04em]">
          {children}
        </h6>
      ),
      ul: ({ children }) => (
        <ul className="my-1.5 list-disc space-y-1 pl-5">{children}</ul>
      ),
      ol: ({ children }) => (
        <ol className="my-1.5 list-decimal space-y-1 pl-5">{children}</ol>
      ),
      li: ({ children }) => <li className="break-words">{children}</li>,
      blockquote: ({ children }) => (
        <blockquote
          className={`my-2 border-l-2 pl-3 ${
            isDark
              ? 'border-green-light text-on-dark'
              : 'border-line-strong text-ink-2'
          }`}
        >
          {children}
        </blockquote>
      ),
      hr: () => (
        <hr
          className={`my-2.5 border-0 border-t ${
            isDark ? 'border-on-dark/25' : 'border-line'
          }`}
        />
      ),
      pre: ({ children }) => {
        const { code, language } = extractCodeBlock(children);
        return <CodeBlock code={code} language={language} tone={tone} />;
      },
      code: ({ children }) => (
        <code
          className={`rounded-xs border px-1 py-0.5 font-mono text-[0.9em] ${
            isDark
              ? 'border-on-dark/20 bg-terminal text-mint'
              : 'border-line bg-paper-2 text-ink'
          }`}
        >
          {children}
        </code>
      ),
      a: ({ href, children }) => {
        const safeHref = safeChatUrl(href, true);
        if (!safeHref) {
          return <span>{children}</span>;
        }
        return (
          <InsideLinkContext.Provider value={true}>
            <a
              href={safeHref}
              target="_blank"
              rel="noopener noreferrer"
              className={`break-all font-bold underline underline-offset-2 ${
                isDark
                  ? 'text-mint decoration-mint/60 hover:text-paper'
                  : 'text-green-600 decoration-green-600/50 hover:text-dark'
              }`}
            >
              {children}
            </a>
          </InsideLinkContext.Provider>
        );
      },
      img: ({ src, alt }) => (
        <MarkdownImageLink
          src={typeof src === 'string' ? src : undefined}
          alt={alt}
          isDark={isDark}
        />
      ),
      table: ({ children }) => (
        <div
          tabIndex={0}
          role="region"
          aria-label={t('markdownTable')}
          className={`my-2 max-w-full overflow-x-auto rounded-sm border focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-green ${
            isDark ? 'border-on-dark/25' : 'border-line'
          }`}
        >
          <table className="w-full border-collapse text-left text-[13px]">
            {children}
          </table>
        </div>
      ),
      thead: ({ children }) => (
        <thead className={isDark ? 'bg-terminal' : 'bg-paper-2'}>
          {children}
        </thead>
      ),
      th: ({ children }) => (
        <th
          className={`border-b px-2.5 py-1.5 text-[12px] font-bold uppercase tracking-[0.04em] ${
            isDark ? 'border-on-dark/25 text-on-dark' : 'border-line text-ink-2'
          }`}
        >
          {children}
        </th>
      ),
      td: ({ children }) => (
        <td
          className={`border-b px-2.5 py-1.5 align-top ${
            isDark ? 'border-on-dark/15' : 'border-line'
          }`}
        >
          {children}
        </td>
      ),
      input: ({
        type,
        checked,
        ...rest
      }: ComponentPropsWithoutRef<'input'>) => {
        if (type === 'checkbox') {
          return (
            <input
              type="checkbox"
              checked={Boolean(checked)}
              readOnly
              disabled
              className="mr-1.5 align-middle accent-green-600"
            />
          );
        }
        return <input type={type} checked={checked} readOnly {...rest} />;
      },
      span: ({ children, ...props }) => {
        if ('data-mention' in props) {
          return (
            <span
              data-mention="true"
              className={`rounded-xs px-1 font-bold ${
                isDark ? 'bg-mint/20 text-mint' : 'bg-blue-tint text-blue'
              }`}
            >
              {children}
            </span>
          );
        }
        return <span {...props}>{children}</span>;
      },
    }),
    [isDark, t, tone],
  );

  return (
    <div
      className={`min-w-0 break-words [&>*:first-child]:mt-0 [&>*:last-child]:mb-0 ${className}`.trim()}
    >
      <ReactMarkdown
        remarkPlugins={remarkPlugins}
        urlTransform={(url, key) => safeChatUrl(url, key === 'href') ?? ''}
        components={components}
      >
        {content}
      </ReactMarkdown>
    </div>
  );
}

function MarkdownImageLink({
  src,
  alt,
  isDark,
}: {
  src?: string;
  alt?: string;
  isDark: boolean;
}) {
  const insideLink = useContext(InsideLinkContext);
  const safeSrc = safeChatUrl(src, false);
  const label = alt?.trim() || safeSrc || '';
  if (!safeSrc || insideLink) {
    return <span>{label}</span>;
  }
  return (
    <a
      href={safeSrc}
      target="_blank"
      rel="noopener noreferrer"
      className={`break-all font-bold underline underline-offset-2 ${
        isDark
          ? 'text-mint decoration-mint/60 hover:text-paper'
          : 'text-green-600 decoration-green-600/50 hover:text-dark'
      }`}
    >
      {label}
    </a>
  );
}
