// Snoop: reads Meet's live captions in this tab and hands the lines to the
// background worker, which uploads them. Captions are free for everyone in a
// call, so this works in any Meet, whoever hosts it.
//
// Meet's markup changes now and then. Every selector lives in SEL, so a fix
// is one place; when nothing matches for a minute, Snoop says so (badge "!"
// and on /admin) instead of failing quietly.
(() => {
  const code = (location.pathname.match(/^\/([a-z]{3}-[a-z]{4}-[a-z]{3})(?:$|[/?])/) || [])[1];
  if (!code) return;

  const SEL = {
    leave: 'button[aria-label*="Leave call" i], button[aria-label*="leave the call" i], [jsname="CQylAd"]',
    captionsOn: 'button[aria-label*="Turn on captions" i]',
    captionsOff: 'button[aria-label*="Turn off captions" i]',
    region: 'div[role="region"][aria-label*="Captions" i], [jsname="dsyhDe"], .a4cQT',
    title: '[data-meeting-title]',
    account: '[aria-label*="Google Account" i][aria-label*="@"]',
  };
  const ACCOUNT_DEFAULT = 'mark@sreday.com';
  const STABLE_MS = 3000;     // a caption unchanged this long is firm enough to keep
  const NO_CAPTIONS_MS = 60000;

  let inCall = false;
  let startedAt = null;
  let joinedAt = 0;
  let problemSent = false;
  const blocks = new Map(); // element -> {speaker, text, committed, changedAt, firstAt}

  const send = (msg) => chrome.runtime.sendMessage({ ...msg, code, startedAt, title: title() }).catch(() => {});

  // Snoop only runs when this tab's Meet is signed in as the traced account
  // (mark@sreday.com), never in a call joined from a personal account.
  // The account menu's label carries the email; Meet's page data is the backup.
  let account = ACCOUNT_DEFAULT;
  chrome.storage.local.get('account').then((s) => { if (s.account) account = s.account.toLowerCase(); });
  function signedIn() {
    const el = document.querySelector(SEL.account);
    const m = (el && el.getAttribute('aria-label').match(/[\w.+-]+@[\w-]+(\.[\w-]+)+/)) ||
      document.documentElement.innerHTML.match(/"oPEP7c":"([^"]+@[^"]+)"/);
    if (!m) return '';
    return (m[1] && m[1].includes('@') ? m[1] : m[0]).toLowerCase();
  }

  function title() {
    const el = document.querySelector(SEL.title);
    const t = el && el.getAttribute('data-meeting-title');
    return t && t !== code ? t : '';
  }

  // The new part of a caption: Meet grows a block while someone talks,
  // corrects recent words, and trims old text off the front of long blocks.
  function newPart(committed, text) {
    if (!committed) return text;
    if (text.startsWith(committed)) return text.slice(committed.length);
    for (let k = Math.min(committed.length, text.length); k >= 20; k--) {
      if (committed.endsWith(text.slice(0, k))) return text.slice(k);
    }
    let i = 0;
    while (i < committed.length && i < text.length && committed[i] === text[i]) i++;
    return text.slice(text.lastIndexOf(' ', i) + 1);
  }

  function readBlocks() {
    const region = document.querySelector(SEL.region);
    if (!region) return null;
    const seen = new Set();
    for (const el of region.children) {
      const lines = (el.innerText || '').split('\n').map((s) => s.trim()).filter(Boolean);
      if (lines.length < 2) continue; // first line is the speaker, the rest is what they said
      const speaker = lines[0];
      const text = lines.slice(1).join(' ');
      seen.add(el);
      const b = blocks.get(el);
      if (!b) {
        blocks.set(el, { speaker, text, committed: '', changedAt: Date.now(), firstAt: Date.now() });
      } else if (b.text !== text || b.speaker !== speaker) {
        Object.assign(b, { speaker, text, changedAt: Date.now() });
      }
    }
    return seen;
  }

  function commit(force) {
    const seen = readBlocks();
    const out = [];
    for (const [el, b] of blocks) {
      const gone = seen && !seen.has(el);
      if (force || gone || Date.now() - b.changedAt >= STABLE_MS) {
        const part = newPart(b.committed, b.text).trim();
        if (part) {
          out.push({ at: new Date(b.committed ? b.changedAt : b.firstAt).toISOString(), speaker: b.speaker, text: part });
          b.committed = b.text;
        }
      }
      if (gone) blocks.delete(el);
    }
    if (out.length) send({ type: 'lines', lines: out });
    return seen;
  }

  function tick() {
    const leave = document.querySelector(SEL.leave);
    if (leave && !inCall) {
      const who = signedIn();
      if (who !== account) return; // another account (or not known yet): leave this call alone
      inCall = true;
      joinedAt = Date.now();
      startedAt = startedAt || new Date().toISOString();
      send({ type: 'joined' });
    }
    if (!inCall) return;
    if (!leave) { // left the call: Meet shows "You left the meeting"
      commit(true);
      inCall = false;
      send({ type: 'ended' });
      startedAt = null; // a rejoin is a new stretch; the server merges it into the same call
      return;
    }
    const off = document.querySelector(SEL.captionsOn);
    if (off && !document.querySelector(SEL.captionsOff)) off.click();
    const seen = commit(false);
    if (!seen && !problemSent && Date.now() - joinedAt > NO_CAPTIONS_MS) {
      problemSent = true;
      send({ type: 'problem', problem: 'Snoop extension found no Meet captions (captions off, or Meet changed its page: update SEL in content.js)' });
    }
  }

  setInterval(tick, 1000);
  window.addEventListener('pagehide', () => { if (inCall) { commit(true); send({ type: 'ended' }); } });
})();
