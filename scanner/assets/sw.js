// Badger Scan service worker: lets the scanner page open again with no
// signal. Always network first, so an online page is never stale; the saved
// copy is only used when the network fails. Leads, sync and exports are
// never cached (scans wait in localStorage instead).
const CACHE = 'badger-scan-v1';

self.addEventListener('install', () => self.skipWaiting());
self.addEventListener('activate', (e) => e.waitUntil(self.clients.claim()));

function cacheable(req) {
    if (req.method !== 'GET') return false;
    const u = new URL(req.url);
    if (u.origin !== self.location.origin) return false;
    return u.pathname === '/assets/jsQR.js' || /^\/s\/[0-9a-f]+$/.test(u.pathname);
}

self.addEventListener('fetch', (e) => {
    if (!cacheable(e.request)) return;
    e.respondWith((async () => {
        const cache = await caches.open(CACHE);
        try {
            const res = await fetch(e.request);
            // Only keep the scanner itself, not the PIN page or errors.
            if (res.ok && !res.redirected && !(res.headers.get('X-Page') === 'pin')) {
                cache.put(e.request, res.clone());
            }
            return res;
        } catch (err) {
            const hit = await cache.match(e.request);
            if (hit) return hit;
            throw err;
        }
    })());
});
