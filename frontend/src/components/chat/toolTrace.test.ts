import { describe, expect, it } from 'vitest';
import {
  buildToolTrace,
  extractToolOutputTail,
  extractToolParameters,
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
      parameters: [
        { key: 'command', value: 'curl -I https://gitlab.inovex.de' },
      ],
      outputTail: {
        lines: ['HTTP 200 OK'],
        hiddenLines: 0,
        totalLines: 1,
      },
    });
    expect(attached?.steps[1]).toMatchObject({
      status: 'done',
      durationMs: 500,
      callCopy: { key: 'screenshotPage' },
      resultCopy: { key: 'pageShot', image: true },
      imagePath: '/api/images/tr2',
      outputTail: {
        lines: ['browser screenshot'],
        hiddenLines: 0,
        totalLines: 1,
      },
    });
    expect(attached?.steps[1]?.parameters).toBeUndefined();
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
    expect(trace.turns[0]?.steps[0]?.outputTail).toBeUndefined();
    expect(trace.turns[0]?.steps[0]?.callCopy).toEqual({
      key: 'finished',
      image: false,
    });
  });

  it('keeps legacy turns distinct across user messages and anchors tool-only turns', () => {
    const messages = [
      makeMessage({
        id: 'leading',
        position: 1,
        senderBotId: 'a',
        content: 'Bash\n{}',
      }),
      makeMessage({
        id: 'first-user',
        position: 2,
        senderType: 'user',
        kind: 'text',
      }),
      makeMessage({
        id: 'anchored',
        position: 3,
        senderBotId: 'a',
        content: 'Bash\n{}',
      }),
      makeMessage({
        id: 'second-user',
        position: 4,
        senderType: 'user',
        kind: 'text',
      }),
      makeMessage({
        id: 'replied',
        position: 5,
        senderBotId: 'a',
        content: 'Bash\n{}',
      }),
      makeMessage({
        id: 'result',
        position: 6,
        senderBotId: 'a',
        content: 'command finished',
      }),
      makeMessage({ id: 'reply', position: 7, senderBotId: 'a', kind: 'text' }),
    ];

    const trace = buildToolTrace({
      messages: messages.reverse(),
      imagePathForMessage: () => '',
    });

    expect(trace.leadingUnattachedTurns.map((turn) => turn.turnKey)).toEqual([
      'legacy-leading',
    ]);
    expect(
      trace.unattachedTurnsByAnchor
        .get('first-user')
        ?.map((turn) => turn.turnKey),
    ).toEqual(['legacy-anchored']);
    expect(trace.turnsByReplyId.get('reply')).toMatchObject({
      turnKey: 'legacy-replied',
      steps: [{ id: 'replied', resultCopy: { key: 'toolNote' } }],
    });
    expect(trace.totalSteps).toBe(3);
  });

  it('pairs interleaved results within their bot turn and keeps unmatched results separate', () => {
    function event(
      position: number,
      bot: string,
      call: string,
      phase: 'call' | 'result',
    ) {
      return makeMessage({
        id: `event-${position}`,
        position,
        senderBotId: bot,
        content: phase === 'call' ? 'Bash\n{}' : `output-${position}`,
        tool: {
          turnId: `turn-${bot}`,
          toolCallId: call,
          phase,
          isError: false,
        },
      });
    }
    const trace = buildToolTrace({
      messages: [
        event(1, 'a', 'shared', 'call'),
        event(2, 'b', 'shared', 'call'),
        event(3, 'a', 'newer', 'call'),
        event(4, 'a', 'shared', 'result'),
        event(5, 'a', 'missing', 'result'),
        event(6, 'b', 'shared', 'result'),
        makeMessage({
          id: 'reply-b',
          position: 7,
          senderBotId: 'b',
          kind: 'text',
        }),
        makeMessage({
          id: 'reply-a',
          position: 8,
          senderBotId: 'a',
          kind: 'text',
        }),
      ],
      imagePathForMessage: () => '',
    });

    expect(trace.turns.map((turn) => turn.turnKey)).toEqual([
      'turn-b',
      'turn-a',
    ]);
    expect(
      trace.turnsByReplyId
        .get('reply-a')
        ?.steps.map((step) => [step.id, step.outputTail?.lines]),
    ).toEqual([
      ['event-1', ['output-4']],
      ['event-3', undefined],
      ['event-5', ['output-5']],
    ]);
    expect(trace.turnsByReplyId.get('reply-b')?.steps).toMatchObject([
      { id: 'event-2', outputTail: { lines: ['output-6'] } },
    ]);
  });

  it('extracts structured parameters and ignores empty or malformed JSON objects', () => {
    expect(
      extractToolParameters(
        'Bash\n{"command":"pytest -q","timeout_s":30,"retry":false,"env":{"CI":true}}',
      ),
    ).toEqual([
      { key: 'command', value: 'pytest -q' },
      { key: 'timeout_s', value: '30' },
      { key: 'retry', value: 'false' },
      { key: 'env', value: '{"CI":true}' },
    ]);
    expect(extractToolParameters('ToolSearch\n{}')).toEqual([]);
    expect(extractToolParameters('Bash\n{not-json')).toEqual([]);
    expect(extractToolParameters('Bash\n["array"]')).toEqual([]);
  });

  it('keeps only the last 5 output lines and strips image source markers', () => {
    const output = [
      'line 1',
      'line 2',
      'line 3',
      'line 4',
      'line 5',
      'line 6',
      'line 7',
      '[Image: source: /home/bot/.ssh/id_rsa]',
    ].join('\n');

    expect(extractToolOutputTail(output)).toEqual({
      lines: ['line 3', 'line 4', 'line 5', 'line 6', 'line 7'],
      hiddenLines: 2,
      totalLines: 7,
    });
    expect(
      extractToolOutputTail('[Image: source: /home/bot/.ssh/id_rsa]'),
    ).toBeUndefined();
  });

  it('formats durations cleanly across milliseconds, seconds, and minutes', () => {
    expect(formatToolDuration(null)).toBeNull();
    expect(formatToolDuration(140)).toBe('140 ms');
    expect(formatToolDuration(1900)).toBe('1.9 s');
    expect(formatToolDuration(125000)).toBe('2 m 5 s');
  });
});
