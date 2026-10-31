// Snout service worker: lets the scanner and the dashboard open again with
// no signal. Always network first, so an online page is never stale; the
// saved copy is only used when the network fails or hangs for 3 s (venue
// Wi-Fi is choppy more often than off). A slow answer still lands in the
// saved copy for next time. Leads, sync and exports are never cached here:
// the pages keep their own data on the device.
const CACHE = 'badger-scan-v1'; // kept from before the rename so phones keep their saved copy
const WAIT = 3000;

self.addEventListener('install', () => self.skipWaiting());
self.addEventListener('activate', (e) => e.waitUntil(self.clients.claim()));

function cacheable(req) {
    if (req.method !== 'GET') return false;
    const u = new URL(req.url);
    if (u.origin !== self.location.origin) return false;
    const p = u.pathname;
    return p === '/assets/jsQR.js' || p === '/assets/snout-logo.png' || p === '/assets/snout-icon.png' ||
        p.startsWith('/assets/test-badge/') ||
        /^\/s\/[0-9a-f]+(\/dashboard|\/qr\.png)?$/.test(p);
}

self.addEventListener('fetch', (e) => {
    if (!cacheable(e.request)) return;
    e.respondWith((async () => {
        const cache = await caches.open(CACHE);
        const net = fetch(e.request).then(async (res) => {
            // Keep only real pages: not the PIN page, redirects or errors.
            if (res.ok && !res.redirected && res.headers.get('X-Page') !== 'pin') {
                await cache.put(e.request, res.clone());
            } else if (res.status === 410) {
                await cache.delete(e.request); // the link is gone: forget it here too
            }
            return res;
        });
        e.waitUntil(net.catch(() => {}));
        const saved = await cache.match(e.request);
        if (!saved) return net; // nothing to fall back to: wait as long as it takes
        const slow = new Promise((resolve) => setTimeout(() => resolve(saved), WAIT));
        // No answer, or the server itself is down (5xx): the saved copy.
        return Promise.race([net.then((r) => (r.status >= 500 ? saved : r), () => saved), slow]);
    })());
});
