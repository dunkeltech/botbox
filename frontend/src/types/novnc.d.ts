// SPDX-License-Identifier: AGPL-3.0-only

declare module '@novnc/novnc' {
  export interface RFBInstance {
    viewOnly: boolean;
    scaleViewport: boolean;
    disconnect: () => void;
    addEventListener: (type: string, listener: (event: Event) => void) => void;
    removeEventListener: (
      type: string,
      listener: (event: Event) => void,
    ) => void;
  }

  export interface RFBOptions {
    wsProtocols?: string[];
  }

  export default class RFB implements RFBInstance {
    constructor(target: HTMLElement, url: string, options?: RFBOptions);
    viewOnly: boolean;
    scaleViewport: boolean;
    disconnect(): void;
    addEventListener(type: string, listener: (event: Event) => void): void;
    removeEventListener(type: string, listener: (event: Event) => void): void;
  }
}
