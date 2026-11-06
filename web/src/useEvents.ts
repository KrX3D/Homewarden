import { useEffect, useRef } from 'react';

// One EventSource for the whole app. Browsers allow only six HTTP/1.1 connections per
// host, and every open stream holds one, so each hook must not open its own.

type Listener = { type: string; fn: (data: any) => void };
const listeners = new Set<Listener>();
const reconnects = new Set<() => void>();
const attached = new Set<string>();
let es: EventSource | null = null;
let opened = false;
let users = 0;

function ensure() {
  if (es) return;
  es = new EventSource('/api/events');
  attached.clear();
  opened = false;
  es.onopen = () => {
    // Every open after the first is a reconnect: callers reload what they missed.
    if (opened) reconnects.forEach((r) => r());
    opened = true;
  };
  listeners.forEach((l) => attach(l.type));
}

function attach(type: string) {
  if (!es || attached.has(type)) return;
  attached.add(type);
  es.addEventListener(type, (e) => {
    let data: unknown;
    try {
      data = JSON.parse((e as MessageEvent).data);
    } catch {
      return;
    }
    listeners.forEach((l) => l.type === type && l.fn(data));
  });
}

/** Subscribes to server-sent events of the given types while the component is mounted. */
export function useEvents(handlers: Record<string, (data: any) => void>, onReconnect?: () => void) {
  const ref = useRef(handlers);
  ref.current = handlers;
  const reconnectRef = useRef(onReconnect);
  reconnectRef.current = onReconnect;
  const types = Object.keys(handlers).sort().join(',');
  useEffect(() => {
    users++;
    ensure();
    const mine: Listener[] = types.split(',').filter(Boolean).map((type) => ({ type, fn: (d) => ref.current[type]?.(d) }));
    mine.forEach((l) => { listeners.add(l); attach(l.type); });
    const rc = () => reconnectRef.current?.();
    reconnects.add(rc);
    return () => {
      mine.forEach((l) => listeners.delete(l));
      reconnects.delete(rc);
      users--;
      if (users === 0 && es) {
        es.close();
        es = null;
      }
    };
  }, [types]);
}
