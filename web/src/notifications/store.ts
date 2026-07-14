import { useSyncExternalStore } from "react";

/**
 * The drawer's open state and the read cursor.
 *
 * Module state, not context: the shell mounts the bell and the drawer as siblings, and a store
 * they both reach without a provider between them keeps that free.
 *
 * Read state is the client's alone. There is no read-state endpoint and inventing one on the
 * client — a fake "read" mutation — would be a lie about where the fact lives. Everything at or
 * below `readThrough` has been seen in this browser; that is the whole model.
 */
const READ_KEY = "flagfish.notifications.read-through";

export interface NotificationsUI {
  open: boolean;
  /** The highest notification id this browser has seen. 0 = nothing seen. */
  readThrough: number;
}

const listeners = new Set<() => void>();

let state: NotificationsUI = { open: false, readThrough: loadReadThrough() };

function loadReadThrough(): number {
  try {
    const raw = localStorage.getItem(READ_KEY);
    const id = raw === null ? 0 : Number(raw);
    return Number.isSafeInteger(id) && id > 0 ? id : 0;
  } catch {
    return 0; // storage can be denied outright (private mode, blocked cookies); memory still works
  }
}

function set(next: Partial<NotificationsUI>): void {
  state = { ...state, ...next };
  for (const l of listeners) l();
}

export function openNotifications(): void {
  set({ open: true });
}

export function closeNotifications(): void {
  set({ open: false });
}

export function toggleNotifications(): void {
  set({ open: !state.open });
}

export function markReadThrough(id: number): void {
  if (id <= state.readThrough) return;
  try {
    localStorage.setItem(READ_KEY, String(id));
  } catch {
    // Not persisted is not unread: the session still tracks it.
  }
  set({ readThrough: id });
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

function getSnapshot(): NotificationsUI {
  return state;
}

export function useNotificationsUI(): NotificationsUI {
  return useSyncExternalStore(subscribe, getSnapshot, getSnapshot);
}
