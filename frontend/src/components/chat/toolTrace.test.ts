import { describe, expect, it } from 'vitest';
import {
  buildToolTrace,
  formatToolDuration,
  type ToolChatMessage,
} from '@/components/chat/toolTrace';

function makeMessage(
  overrides: Partial<ToolChatMessage> &
    Pick<ToolChatMessage, 'id' | 'position'>,
): ToolChatMessage {
  return {
    senderType: 'bot',
    kind: 'tool',
    content: '',
    createdAt: '2026-11-17T14:23:00Z',
    ...overrides,
  };
}

describe('buildToolTrace', () => {
  it('pairs tool_call and tool_result by turnId and toolCallId and attaches to bot reply', () => {
    const messages: ToolChatMessage[] = [
      makeMessage({
        id: 'u1',
        position: 1,
        senderType: 'user',
        kind: 'text',
        content: 'Check gitlab',
        createdAt: '2026-11-17T14:23:00Z',
      }),
      makeMessage({
        id: 'tc1',
        position: 2,
        senderBotId: 'bot-1',
        content: 'Bash\n{"command":"curl -I https://gitlab.inovex.de"}',
        createdAt: '2026-11-17T14:23:01Z',
        tool: {
          turnId: 'turn-1',
          toolCallId: 'call-1',
          phase: 'call',
          isError: false,
        },
      }),
      makeMessage({
        id: 'tr1',
        position: 3,
        senderBotId: 'bot-1',
        content: 'HTTP 200 OK',
        createdAt: '2026-11-17T14:23:02.2Z',
        tool: {
          turnId: 'turn-1',
          toolCallId: 'call-1',
          phase: 'result',
          isError: false,
        },
      }),
      makeMessage({
        id: 'tc2',
        position: 4,
        senderBotId: 'bot-1',
        content: 'mcp__botbox-desktop__browser_screenshot\n{}',
        createdAt: '2026-11-17T14:23:03Z',
        tool: {
          turnId: 'turn-1',
          toolCallId: 'call-2',
          phase: 'call',
          isError: false,
        },
      }),
      makeMessage({
        id: 'tr2',
        position: 5,
        senderBotId: 'bot-1',
        content:
          '[Image: source: /home/bot/.claude/projects/-home-bot/fe505481-b639-48b3-95ab-6e1db702084f/tool-results/shot.png]\nbrowser screenshot',
        createdAt: '2026-11-17T14:23:03.5Z',
        tool: {
          turnId: 'turn-1',
          toolCallId: 'call-2',
          phase: 'result',
          isError: false,
        },
      }),
      makeMessage({
        id: 'b1',
        position: 6,
        senderBotId: 'bot-1',
        kind: 'text',
        content: 'Verified HTTPS access.',
        createdAt: '2026-11-17T14:23:04Z',
      }),
    ];

    const trace = buildToolTrace({
      messages,
      imagePathForMessage: (id) => `/api/images/${id}`,
    });

    expect(trace.chatMessages.map((m) => m.id)).toEqual(['u1', 'b1']);
    expect(trace.totalSteps).toBe(2);
    expect(trace.runningSteps).toBe(0);
    expect(trace.errorSteps).toBe(0);

    const attached = trace.turnsByReplyId.get('b1');
    expect(attached).toBeDefined();
    expect(attached?.steps).toHaveLength(2);
    expect(attached?.steps[0]).toMatchObject({
      status: 'done',
      durationMs: 1200,
      callCopy: { key: 'usingTool', values: { name: 'Bash' } },
      resultCopy: { key: 'toolNote', values: { note: 'HTTP 200 OK' } },
    });
    expect(attached?.steps[1]).toMatchObject({
      status: 'done',
      durationMs: 500,
      callCopy: { key: 'screenshotPage' },
      resultCopy: { key: 'pageShot', image: true },
      imagePath: '/api/images/tr2',
    });
  });

  it('tracks active running steps and error steps with error codes', () => {
    const messages: ToolChatMessage[] = [
      makeMessage({
        id: 'tc-err',
        position: 1,
        senderBotId: 'bot-1',
        content: 'Bash\n{}',
        createdAt: '2026-11-17T14:25:00Z',
        tool: {
          turnId: 'turn-2',
          toolCallId: 'c1',
          phase: 'call',
          isError: false,
        },
      }),
      makeMessage({
        id: 'tr-err',
        position: 2,
        senderBotId: 'bot-1',
        content: 'Blocked',
        createdAt: '2026-11-17T14:25:01Z',
        tool: {
          turnId: 'turn-2',
          toolCallId: 'c1',
          phase: 'result',
          isError: true,
          errorCode: 'egress.blocked',
        },
      }),
      makeMessage({
        id: 'tc-run',
        position: 3,
        senderBotId: 'bot-1',
        content: 'Bash\n{}',
        createdAt: '2026-11-17T14:25:02Z',
        tool: {
          turnId: 'turn-2',
          toolCallId: 'c2',
          phase: 'call',
          isError: false,
        },
      }),
    ];

    const trace = buildToolTrace({
      messages,
      turns: [
        { id: 'turn-2', status: 'running', queueAhead: 0, errorCode: '' },
      ],
      imagePathForMessage: (id) => `/api/images/${id}`,
    });

    expect(trace.totalSteps).toBe(2);
    expect(trace.runningSteps).toBe(1);
    expect(trace.errorSteps).toBe(1);
    expect(trace.turns[0]?.status).toBe('error');
    expect(trace.turns[0]?.steps[0]).toMatchObject({
      status: 'error',
      errorCode: 'egress.blocked',
    });
    expect(trace.turns[0]?.steps[1]?.status).toBe('running');
    expect(trace.leadingUnattachedTurns).toHaveLength(1);
  });

  it('never exposes disallowed sandbox paths as imagePath', () => {
    const trace = buildToolTrace({
      messages: [
        makeMessage({
          id: 'tr-bad',
          position: 1,
          content: '[Image: source: /home/bot/.ssh/id_rsa]',
        }),
      ],
      imagePathForMessage: (id) => `/api/images/${id}`,
    });

    expect(trace.turns[0]?.steps[0]?.imagePath).toBeUndefined();
    expect(trace.turns[0]?.steps[0]?.callCopy).toEqual({
      key: 'finished',
      image: false,
    });
  });

  it('formats durations cleanly across milliseconds, seconds, and minutes', () => {
    expect(formatToolDuration(null)).toBeNull();
    expect(formatToolDuration(140)).toBe('140 ms');
    expect(formatToolDuration(1900)).toBe('1.9 s');
    expect(formatToolDuration(125000)).toBe('2 m 5 s');
  });
});
