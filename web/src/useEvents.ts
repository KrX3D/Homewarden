import { useEffect, useRef } from 'react';

/**
 * Subscribes to server-sent events. One EventSource per hook; the browser reconnects
 * by itself, and onReconnect lets the caller reload whatever it may have missed.
 */
export function useEvents(handlers: Record<string, (data: any) => void>, onReconnect?: () => void) {
  const ref = useRef(handlers);
  ref.current = handlers;
  const reconnectRef = useRef(onReconnect);
  reconnectRef.current = onReconnect;
  const types = Object.keys(handlers).sort().join(',');
  useEffect(() => {
    const es = new EventSource('/api/events');
    let opened = false;
    es.onopen = () => {
      if (opened) reconnectRef.current?.();
      opened = true;
    };
    for (const type of types.split(',')) {
      es.addEventListener(type, (e) => {
        try {
          ref.current[type]?.(JSON.parse((e as MessageEvent).data));
        } catch {
          /* ignore malformed */
        }
      });
    }
    return () => es.close();
  }, [types]);
}
