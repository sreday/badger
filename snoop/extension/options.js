const $ = (id) => document.getElementById(id);

async function load() {
  const s = await chrome.storage.local.get(['account', 'server', 'token', 'calls', 'lastFlush', 'lastError']);
  $('server').value = s.server || 'https://scan.sreday.com/snoop';
  $('token').value = s.token || '';
  $('account').value = s.account || 'mark@sreday.com';
  $('upload').innerHTML = s.lastError
    ? '<span class="bad">' + esc(s.lastError) + '</span>'
    : s.lastFlush ? 'Last upload check ' + new Date(s.lastFlush).toLocaleTimeString() : 'Nothing uploaded yet';
  const list = Object.values(s.calls || {}).sort((a, b) => b.startedAt.localeCompare(a.startedAt));
  $('calls').innerHTML = list.length ? list.map((c) =>
    '<li>' + esc(c.title || c.code) + ' <span class="muted">' + new Date(c.startedAt).toLocaleString() + ' · ' + c.lines.length + ' lines · ' +
    (c.ended ? (c.notesSent ? 'notes sent' : c.notesFailed ? '<span class="bad">no notes: ' + esc(c.notesError || '') + '</span>' : 'writing notes') : 'in call') +
    (c.problem ? ' · <span class="bad">' + esc(c.problem) + '</span>' : '') + '</span></li>').join('') : '<li class="muted">None</li>';
  try {
    const a = await aiStatus();
    $('ai').innerHTML = 'Summarizer: <b>' + a.summarizer + '</b> · Prompt: <b>' + a.prompt + '</b>' +
      (a.summarizer === 'available' ? ' <span class="good">ready</span>' : a.summarizer === 'missing' ? ' <span class="bad">this Chrome has no built-in AI (needs Chrome 138+ on a supported laptop)</span>' : '');
    $('setup').style.display = a.summarizer === 'available' && (a.prompt === 'available' || a.prompt === 'missing' || a.prompt === 'unavailable') ? 'none' : '';
  } catch (e) { $('ai').textContent = String(e); }
}

function esc(s) { return String(s).replace(/[&<>"]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c])); }

$('save').onclick = async () => {
  const server = $('server').value.trim().replace(/\/+$/, '');
  const token = $('token').value.trim();
  const origin = new URL(server).origin + '/*';
  if (!(await chrome.permissions.contains({ origins: [origin] }))) await chrome.permissions.request({ origins: [origin] });
  const account = ($('account').value.trim() || 'mark@sreday.com').toLowerCase();
  await chrome.storage.local.set({ server, token, account });
  try {
    const r = await fetch(server + '/ping', { headers: { Authorization: 'Bearer ' + token } });
    const j = r.ok ? await r.json() : null;
    $('saved').innerHTML = j ? '<span class="good">Connected as ' + esc(j.device) + '</span>' : '<span class="bad">Server said ' + r.status + '</span>';
  } catch (e) { $('saved').innerHTML = '<span class="bad">' + esc(e.message) + '</span>'; }
};

// The model downloads once (about 2 GB); Chrome wants a click for that.
$('setup').onclick = async () => {
  $('ai').textContent = 'Downloading the model… keep this open';
  const mon = (m) => m.addEventListener('downloadprogress', (e) => { $('ai').textContent = 'Downloading the model… ' + Math.round(e.loaded * 100) + '%'; });
  try {
    if (self.Summarizer) (await Summarizer.create({ monitor: mon })).destroy();
    if (self.LanguageModel) (await LanguageModel.create({ monitor: mon })).destroy();
  } catch (e) { $('ai').textContent = String(e); return; }
  load();
};

// Notes written here (a click lets Chrome use the model even if the
// offscreen page can't).
chrome.runtime.onMessage.addListener((msg, sender, reply) => {
  if (msg.type !== 'summarise' || msg.target !== 'options') return;
  summarise(msg.lines, msg.title).then((notes) => reply({ notes }), (e) => reply({ error: String(e.message || e) }));
  return true;
});

$('now').onclick = async () => {
  $('nowmsg').textContent = 'Working…';
  const r = await chrome.runtime.sendMessage({ type: 'summarise-now' });
  $('nowmsg').textContent = r && r.ok ? 'Done' : 'Failed: ' + (r && r.error);
  load();
};

load();
