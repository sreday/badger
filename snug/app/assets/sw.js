// Cloud Badger service worker (hosted in Snout): the event page and every
// badge picture open again with no signal, so the door keeps printing when
// venue Wi-Fi dies. Network first, so an online page is never stale; the
// saved copy is used when the network fails, the server is down (5xx), or
// nothing comes back in time (8 s for the guest list, which waits on Luma;
// 3 s for a badge). A slow answer still lands in the saved copy.
const CACHE = 'badger-cloud-v1';
const SCOPE = new URL(self.registration.scope).pathname.replace(/\/$/, ''); // "/badger"

self.addEventListener('install', () => self.skipWaiting());
self.addEventListener('activate', (e) => e.waitUntil(self.clients.claim()));

function wait(req) {
    if (req.method !== 'GET') return 0;
    const u = new URL(req.url);
    if (u.origin !== self.location.origin || !u.pathname.startsWith(SCOPE + '/')) return 0;
    const p = u.pathname.slice(SCOPE.length);
    if (p === '/' || /^\/event\/[^/]+$/.test(p)) return 8000;
    if (/^\/view\/[^/]+\/[^/]+$/.test(p)) return 3000;
    return 0;
}

self.addEventListener('fetch', (e) => {
    const ms = wait(e.request);
    if (!ms) return;
    e.respondWith((async () => {
        const cache = await caches.open(CACHE);
        const net = fetch(e.request).then(async (res) => {
            if (res.ok && !res.redirected) await cache.put(e.request.url, res.clone());
            return res;
        });
        e.waitUntil(net.catch(() => {}));
        const saved = await cache.match(e.request.url);
        if (!saved) return net;
        const slow = new Promise((resolve) => setTimeout(() => resolve(saved), ms));
        return Promise.race([net.then((r) => (r.status >= 500 ? saved : r), () => saved), slow]);
    })());
});
