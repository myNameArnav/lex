// Browser APIs that TypeScript's lib.dom doesn't include, and a few DOM
// lookups typed for how the UI uses them.

declare global {
  interface Window {
    // Safari's MediaSource for iOS/iPadOS 17.1+, and the old prefixed one.
    ManagedMediaSource?: typeof MediaSource;
    WebKitMediaSource?: typeof MediaSource;
  }

  interface HTMLVideoElement {
    // iOS Safari: fullscreen for the video alone (no Fullscreen API on iPhone).
    webkitEnterFullscreen?(): void;
  }

  // Properties the UI keeps on its own elements.
  interface HTMLElement {
    // A view's root (admin.js): keeps the view up while the next route loads.
    pendingRoute?: (section: string, id: string) => boolean;
    // An *arr connection row (admin/server.js): checks its status again.
    recheck?: () => void;
  }

  // The UI only queries its own HTML, never SVG or MathML, so selector
  // lookups return HTMLElement (lib.dom says Element). Tag-name lookups keep
  // lib.dom's precise types.
  interface ParentNode {
    querySelector<K extends keyof HTMLElementTagNameMap>(selectors: K): HTMLElementTagNameMap[K] | null;
    querySelector<E extends Element = HTMLElement>(selectors: string): E | null;
    querySelectorAll<K extends keyof HTMLElementTagNameMap>(selectors: K): NodeListOf<HTMLElementTagNameMap[K]>;
    querySelectorAll<E extends Element = HTMLElement>(selectors: string): NodeListOf<E>;
  }
  interface Element {
    closest<K extends keyof HTMLElementTagNameMap>(selectors: K): HTMLElementTagNameMap[K] | null;
    closest<E extends Element = HTMLElement>(selectors: string): E | null;
  }
}

export {};
