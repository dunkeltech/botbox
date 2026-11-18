// SPDX-License-Identifier: AGPL-3.0-only

import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from '@testing-library/react';
import { NextIntlClientProvider } from 'next-intl';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { BrokerTakeoverModal } from '@/components/chat/BrokerTakeoverModal';
import {
  SecretRequestCard,
  type SecretRequestSummary,
} from '@/components/chat/SecretRequestCard';
import { SecretRequestModal } from '@/components/chat/SecretRequestModal';
import { ApiClientError, requestBlob, requestJson } from '@/lib/api-client';
import type { WorkspaceRole } from '@/types/api';
import messages from '../../messages/en.json';

vi.mock('@/lib/api-client', async () => {
  const actual =
    await vi.importActual<typeof import('@/lib/api-client')>(
      '@/lib/api-client',
    );
  return { ...actual, requestJson: vi.fn(), requestBlob: vi.fn() };
});

const request = vi.mocked(requestJson);
const requestBinary = vi.mocked(requestBlob);

const baseSummary: SecretRequestSummary = {
  secretRequestId: 'req-1',
  kind: 'cli_egress',
  status: 'pending',
  adapterId: 'github_api',
  adapterLabel: 'GitHub REST / GraphQL API',
  targetOrigin: 'https://api.github.com:443',
  headerScheme: 'Authorization: Bearer <secret>',
  envPlaceholderName: 'GH_TOKEN',
  placeholderValue: 'botbox-placeholder-abc123',
  reason: 'List open releases for verification',
};

function wrap(ui: React.ReactNode) {
  return render(
    <NextIntlClientProvider locale="en" messages={messages}>
      {ui}
    </NextIntlClientProvider>,
  );
}

describe('SecretRequestCard and SecretRequestModal', () => {
  beforeEach(() => {
    request.mockReset();
    requestBinary.mockReset();
  });

  afterEach(() => {
    cleanup();
  });

  it('renders all status labels and hides action buttons for viewers', () => {
    const statuses: Array<{
      status: SecretRequestSummary['status'];
      label: string;
    }> = [
      { status: 'pending', label: 'Waiting for secret' },
      { status: 'fulfilled', label: 'Provided · hidden from AI' },
      { status: 'denied', label: 'Denied' },
      { status: 'expired', label: 'Expired' },
      { status: 'cancelled', label: 'Cancelled' },
      { status: 'failed', label: 'Failed' },
    ];

    for (const item of statuses) {
      const { unmount } = wrap(
        <SecretRequestCard
          summary={{ ...baseSummary, status: item.status }}
          role="viewer"
          onProvide={vi.fn()}
          onDeny={vi.fn()}
        />,
      );
      expect(screen.getByText(item.label)).toBeTruthy();
      expect(
        screen.queryByRole('button', { name: /Provide secret securely/i }),
      ).toBeNull();
      unmount();
    }
  });

  it('disables browser_login secret entry when capability is unsupported', () => {
    wrap(
      <SecretRequestCard
        summary={{ ...baseSummary, kind: 'browser_login' }}
        role="owner"
        onProvide={vi.fn()}
        onDeny={vi.fn()}
      />,
    );
    expect(screen.getByText(/isolated authentication broker/i)).toBeTruthy();
    expect(
      screen.queryByRole('button', { name: /Provide secret securely/i }),
    ).toBeNull();
  });

  it('restricts workspace_secret scope for members while allowing owners', () => {
    const renderModal = (role: WorkspaceRole) =>
      wrap(
        <SecretRequestModal
          workspaceId="ws-1"
          botName="Atlas"
          role={role}
          summary={baseSummary}
          onClose={vi.fn()}
          onCompleted={vi.fn()}
        />,
      );

    const { unmount } = renderModal('member');
    const wsRadioMember = screen.getByRole<HTMLInputElement>('radio', {
      name: /Save as workspace secret/i,
    });
    expect(wsRadioMember.disabled).toBe(true);
    expect(
      screen.getByRole<HTMLInputElement>('radio', {
        name: /One-time ephemeral proxy lease/i,
      }).disabled,
    ).toBe(false);
    expect(
      screen.getByRole<HTMLInputElement>('radio', {
        name: /Save as bot secret/i,
      }).disabled,
    ).toBe(false);
    unmount();

    renderModal('owner');
    const wsRadioOwner = screen.getByRole<HTMLInputElement>('radio', {
      name: /Save as workspace secret/i,
    });
    expect(wsRadioOwner.disabled).toBe(false);
  });

  it('submits write-only secret, clears field, and surfaces conflict replace option on 409', async () => {
    const onClose = vi.fn();
    const onCompleted = vi.fn();
    request.mockRejectedValueOnce(
      new ApiClientError(409, 'secret_request.rule_conflict', 'conflict'),
    );
    request.mockResolvedValueOnce({
      secretRequest: { ...baseSummary, status: 'fulfilled' },
    });

    wrap(
      <SecretRequestModal
        workspaceId="ws-1"
        botName="Atlas"
        role="owner"
        summary={baseSummary}
        onClose={onClose}
        onCompleted={onCompleted}
      />,
    );

    const input = screen.getByLabelText<HTMLInputElement>(
      /Secret or API token value/i,
    );
    fireEvent.change(input, { target: { value: 'ghp_first_attempt' } });
    fireEvent.click(screen.getByRole('radio', { name: /Save as bot secret/i }));
    fireEvent.click(
      screen.getByRole('button', { name: /Save to egress proxy/i }),
    );

    await waitFor(() => {
      expect(
        screen.getByText(
          /A persistent secret already exists for this adapter/i,
        ),
      ).toBeTruthy();
    });
    expect(input.value).toBe('');

    const replaceCheckbox = screen.getByRole('checkbox', {
      name: /Replace existing conflicting secret rule/i,
    });
    fireEvent.click(replaceCheckbox);
    fireEvent.change(input, { target: { value: 'ghp_second_attempt' } });
    fireEvent.click(
      screen.getByRole('button', { name: /Save to egress proxy/i }),
    );

    await waitFor(() => {
      expect(onCompleted).toHaveBeenCalledTimes(1);
    });
    const lastCall = request.mock.calls.at(-1);
    expect(lastCall?.[0]).toBe(
      '/api/workspaces/ws-1/secret-requests/req-1/fulfill',
    );
    expect(lastCall?.[1]).toEqual({
      method: 'POST',
      body: {
        secretValue: 'ghp_second_attempt',
        username: undefined,
        scope: 'bot_secret',
        replaceExisting: true,
      },
    });
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it('closes on Escape and clears entered secret value', () => {
    const onClose = vi.fn();
    wrap(
      <SecretRequestModal
        workspaceId="ws-1"
        botName="Atlas"
        role="member"
        summary={baseSummary}
        onClose={onClose}
        onCompleted={vi.fn()}
      />,
    );

    const dialog = screen.getByRole('dialog');
    const input = screen.getByLabelText<HTMLInputElement>(
      /Secret or API token value/i,
    );
    fireEvent.change(input, { target: { value: 'ghp_unsaved' } });
    fireEvent.keyDown(dialog, { key: 'Escape' });

    expect(onClose).toHaveBeenCalledTimes(1);
    expect(input.value).toBe('');
  });

  it('enables browser_login broker submission and transitions into MFA takeover', async () => {
    const brokerSummary: SecretRequestSummary = {
      secretRequestId: 'req-browser-1',
      kind: 'browser_login',
      status: 'pending',
      adapterId: 'github_web',
      adapterLabel: 'GitHub Web Sign-In',
      targetOrigin: 'https://github.com:443',
      reason: 'Sign in to check deployment status',
      brokerEnabled: true,
      brokerSessionId: 'sess-1234',
    };

    const onProvide = vi.fn();
    const onTakeover = vi.fn();
    const { unmount } = wrap(
      <SecretRequestCard
        summary={brokerSummary}
        role="member"
        onProvide={onProvide}
        onTakeover={onTakeover}
        onDeny={vi.fn()}
      />,
    );
    expect(
      screen.getByText(/Isolated broker session: sess-1234/i),
    ).toBeTruthy();
    fireEvent.click(
      screen.getByRole('button', { name: /Provide secret securely/i }),
    );
    expect(onProvide).toHaveBeenCalledTimes(1);
    fireEvent.click(
      screen.getByRole('button', { name: /Open broker takeover/i }),
    );
    expect(onTakeover).toHaveBeenCalledTimes(1);
    unmount();

    const onClose = vi.fn();
    const onCompleted = vi.fn();
    const onOpenTakeover = vi.fn();
    request.mockResolvedValueOnce({
      secretRequest: brokerSummary,
      mfaRequired: true,
    });

    wrap(
      <SecretRequestModal
        workspaceId="ws-1"
        botName="Atlas"
        role="member"
        summary={brokerSummary}
        onClose={onClose}
        onCompleted={onCompleted}
        onOpenTakeover={onOpenTakeover}
      />,
    );

    expect(
      screen.queryByRole('radio', { name: /Save as bot secret/i }),
    ).toBeNull();
    expect(
      screen.getByRole<HTMLInputElement>('radio', {
        name: /Isolated broker session only/i,
      }).checked,
    ).toBe(true);

    fireEvent.change(
      screen.getByLabelText<HTMLInputElement>(/Secret or API token value/i),
      { target: { value: 'web-pw-123' } },
    );
    fireEvent.click(
      screen.getByRole('button', { name: /Submit to broker & log in/i }),
    );

    await waitFor(() => {
      expect(onOpenTakeover).toHaveBeenCalledWith(brokerSummary);
    });
    expect(onCompleted).toHaveBeenCalledTimes(1);
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it('runs human-only BrokerTakeoverModal frame, input, and completion flow', async () => {
    const createDesc = Object.getOwnPropertyDescriptor(URL, 'createObjectURL');
    const revokeDesc = Object.getOwnPropertyDescriptor(URL, 'revokeObjectURL');
    Object.defineProperty(URL, 'createObjectURL', {
      configurable: true,
      writable: true,
      value: vi.fn(() => 'blob:broker-frame-1'),
    });
    Object.defineProperty(URL, 'revokeObjectURL', {
      configurable: true,
      writable: true,
      value: vi.fn(),
    });
    try {
      const brokerSummary: SecretRequestSummary = {
        secretRequestId: 'req-browser-2',
        kind: 'browser_login',
        status: 'pending',
        adapterId: 'github_web',
        adapterLabel: 'GitHub Web Sign-In',
        targetOrigin: 'https://github.com:443',
        reason: 'Complete MFA',
        brokerEnabled: true,
        brokerSessionId: 'sess-5678',
      };

      request.mockResolvedValueOnce({
        secretRequest: { ...brokerSummary, takeoverActive: true },
        brokerSessionId: 'sess-5678',
        origin: 'https://github.com:443',
        takeoverActive: true,
        takeoverTicket: 'signed-tk-1',
      });
      requestBinary.mockResolvedValue(new Blob(['png'], { type: 'image/png' }));

      const onCompleted = vi.fn();
      const onClose = vi.fn();
      wrap(
        <BrokerTakeoverModal
          workspaceId="ws-1"
          botName="Atlas"
          role="member"
          summary={brokerSummary}
          onClose={onClose}
          onCompleted={onCompleted}
        />,
      );

      await waitFor(() => {
        expect(
          screen.getByAltText(/Isolated authentication broker viewport/i),
        ).toBeTruthy();
      });
      expect(requestBinary).toHaveBeenCalledWith(
        '/api/workspaces/ws-1/secret-requests/req-browser-2/broker-takeover/frame?ticket=signed-tk-1',
      );

      request.mockResolvedValueOnce({
        secretRequest: { ...brokerSummary, takeoverActive: true },
        brokerSessionId: 'sess-5678',
        origin: 'https://github.com:443',
        takeoverActive: true,
      });
      fireEvent.change(
        screen.getByLabelText(/Type into active broker field/i),
        { target: { value: '123456' } },
      );
      fireEvent.click(screen.getByRole('button', { name: /Send input/i }));

      await waitFor(() => {
        expect(request).toHaveBeenCalledWith(
          '/api/workspaces/ws-1/secret-requests/req-browser-2/broker-takeover/input',
          {
            method: 'POST',
            body: { kind: 'type', text: '123456' },
          },
        );
      });

      request.mockResolvedValueOnce({
        secretRequest: { ...brokerSummary, status: 'fulfilled' },
      });
      fireEvent.click(
        screen.getByRole('button', { name: /Complete login & hand back/i }),
      );

      await waitFor(() => {
        expect(onCompleted).toHaveBeenCalledTimes(1);
      });
      expect(onClose).toHaveBeenCalledTimes(1);
    } finally {
      if (createDesc) {
        Object.defineProperty(URL, 'createObjectURL', createDesc);
      } else {
        delete (URL as { createObjectURL?: unknown }).createObjectURL;
      }
      if (revokeDesc) {
        Object.defineProperty(URL, 'revokeObjectURL', revokeDesc);
      } else {
        delete (URL as { revokeObjectURL?: unknown }).revokeObjectURL;
      }
    }
  });
});
