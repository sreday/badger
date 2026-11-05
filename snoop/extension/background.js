// Snoop background: keeps each call's caption lines on this laptop and
// uploads them to the Snoop server every 30 s (and at the end), so a
// Wi-Fi blip never loses a line. When a call ends it writes the notes
// with Chrome's built-in AI in an offscreen page (summarise.js).

const DEFAULT_SERVER = 'https://scan.sreday.com/snoop';
const KEEP_DAYS = 3;

async function settings() {
  const s = await chrome.storage.local.get(['server', 'token']);
  return { server: (s.server || DEFAULT_SERVER).replace(/\/+$/, ''), token: s.token || '' };
}

async function calls() { return (await chrome.storage.local.get('calls')).calls || {}; }
async function saveCalls(c) { await chrome.storage.local.set({ calls: c }); }

// One message at a time: storage read-modify-write must not interleave.
let chain = Promise.resolve();
const serial = (fn) => (chain = chain.then(fn, fn));

chrome.runtime.onMessage.addListener((msg, sender, reply) => {
  if (msg.type === 'summarise-now') {
    serial(() => summarisePending(true)).then(() => reply({ ok: true }), (e) => reply({ ok: false, error: String(e) }));
    return true;
  }
  if (!msg.code || !msg.startedAt) return;
  serial(async () => {
    const all = await calls();
    const key = msg.code + '|' + msg.startedAt;
    const c = all[key] || (all[key] = { code: msg.code, startedAt: msg.startedAt, title: '', lines: [], sent: 0, ended: false, notes: null, notesSent: false });
    if (msg.title) c.title = msg.title;
    if (sender.tab) c.tabId = sender.tab.id;
    if (msg.type === 'lines') c.lines.push(...msg.lines);
    if (msg.type === 'problem') { c.problem = msg.problem; chrome.action.setBadgeText({ text: '!' }); }
    if (msg.type === 'ended') c.ended = true;
    c.touched = Date.now();
    await saveCalls(all);
    if (msg.type === 'ended' || msg.type === 'problem') await flush();
    if (msg.type === 'ended') await summarisePending(false);
  });
});

// A Meet tab closed without the leave button: that call has ended too.
chrome.tabs.onRemoved.addListener((tabId) => serial(async () => {
  const all = await calls();
  let hit = false;
  for (const c of Object.values(all)) if (c.tabId === tabId && !c.ended) { c.ended = true; hit = true; }
  if (!hit) return;
  await saveCalls(all);
  await flush();
  await summarisePending(false);
}));

chrome.alarms.create('flush', { periodInMinutes: 0.5 });
chrome.alarms.onAlarm.addListener((a) => { if (a.name === 'flush') serial(async () => { await flush(); await summarisePending(false); }); });

async function post(body) {
  const { server, token } = await settings();
  if (!token) throw new Error('no token: open the Snoop extension options');
  const r = await fetch(server + '/ingest', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Authorization: 'Bearer ' + token },
    body: JSON.stringify(body),
  });
  if (!r.ok) throw new Error('Snoop server ' + r.status + ': ' + (await r.text()).slice(0, 200));
}

// flush uploads whatever each call hasn't sent yet. Lines stay on the laptop
// (for the notes) until the call is done and a few days old.
async function flush() {
  const all = await calls();
  let err = null;
  for (const [key, c] of Object.entries(all)) {
    const fresh = c.lines.slice(c.sent);
    const due = fresh.length || (c.ended && !c.endSent) || (c.notes && !c.notesSent) || (c.problem && !c.problemSent);
    if (due) {
      try {
        await post({
          meeting_code: c.code, title: c.title, started_at: c.startedAt, lines: fresh,
          ended: c.ended && !!(c.notes || c.notesFailed), notes: c.notes || undefined, problem: c.problem || '',
        });
        c.sent += fresh.length;
        if (c.ended) c.endSent = !!(c.notes || c.notesFailed);
        if (c.notes) c.notesSent = true;
        if (c.problem) c.problemSent = true;
      } catch (e) { err = e; break; }
    }
    if (c.ended && (c.notesSent || c.notesFailed) && c.sent >= c.lines.length && Date.now() - c.touched > KEEP_DAYS * 864e5) delete all[key];
  }
  await saveCalls(all);
  await chrome.storage.local.set({ lastFlush: Date.now(), lastError: err ? String(err.message || err) : '' });
  chrome.action.setBadgeText({ text: err ? '!' : '' });
}

// --- Notes, on this laptop -------------------------------------------------

async function offscreen() {
  if (await chrome.offscreen.hasDocument()) return;
  await chrome.offscreen.createDocument({
    url: 'offscreen.html',
    reasons: ['DOM_PARSER'],
    justification: "Summarise the call's transcript with Chrome's built-in AI",
  });
}

// summarisePending writes notes for ended calls that have none yet.
// manual = pressed in the options page, where Chrome lets the model download.
async function summarisePending(manual) {
  const all = await calls();
  const todo = Object.values(all).filter((c) => c.ended && !c.notes && (!c.notesFailed || manual) && c.lines.length);
  if (!todo.length) return;
  for (const c of todo) {
    let res;
    try {
      if (manual) res = await chrome.runtime.sendMessage({ type: 'summarise', target: 'options', lines: c.lines, title: c.title });
      if (!res) { await offscreen(); res = await chrome.runtime.sendMessage({ type: 'summarise', target: 'offscreen', lines: c.lines, title: c.title }); }
    } catch (e) { res = { error: String(e) }; }
    const fresh = await calls();
    const s = fresh[c.code + '|' + c.startedAt];
    if (!s) continue;
    if (res && res.notes) { s.notes = res.notes; s.notesFailed = false; }
    else { s.notesFailed = true; s.notesError = (res && res.error) || 'no answer from the AI page'; }
    await saveCalls(fresh);
  }
  await flush();
}
