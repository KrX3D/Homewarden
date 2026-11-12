// Homewarden service worker: only makes the app installable. Every request goes to
// the network as usual; nothing is cached, so the app never shows stale data.
self.addEventListener('install', () => self.skipWaiting());
self.addEventListener('activate', (e) => e.waitUntil(self.clients.claim()));
self.addEventListener('fetch', () => {});
