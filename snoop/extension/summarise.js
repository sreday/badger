// Notes from a transcript with Chrome's built-in AI (Gemini Nano, on this
// laptop; nothing leaves it). Summarizer API for the summary, Prompt API
// for next steps. Long calls go in chunks.
const CHUNK = 12000; // characters; well inside Nano's input quota

async function aiStatus() {
  const s = {};
  s.summarizer = self.Summarizer ? await Summarizer.availability() : 'missing';
  s.prompt = self.LanguageModel ? await LanguageModel.availability() : 'missing';
  return s;
}

function chunks(lines) {
  const out = [];
  let cur = '';
  for (const l of lines) {
    const t = l.speaker + ': ' + l.text + '\n';
    if (cur.length + t.length > CHUNK && cur) { out.push(cur); cur = ''; }
    cur += t;
  }
  if (cur) out.push(cur);
  return out;
}

async function summarise(lines, title) {
  const status = await aiStatus();
  if (status.summarizer === 'missing' || status.summarizer === 'unavailable') {
    throw new Error("Chrome's on-device AI isn't available on this laptop (chrome://on-device-internals)");
  }
  const context = 'A business call' + (title ? ' titled "' + title + '"' : '') +
    ' at a company that runs tech conferences (SREday, LLMday, PLATFORMday, Conf42). The text is a live-caption transcript, one "Speaker: words" per line.';
  const parts = chunks(lines);

  const sum = await Summarizer.create({ type: 'tldr', format: 'plain-text', length: 'medium', sharedContext: context });
  let pieces = [];
  for (const p of parts) pieces.push(await sum.summarize(p));
  let summary = pieces.join('\n\n');
  if (pieces.length > 1) summary = await sum.summarize(summary, { context: 'These are summaries of consecutive parts of one call. Summarise the whole call.' });
  sum.destroy();

  let steps = [];
  if (status.prompt !== 'missing' && status.prompt !== 'unavailable') {
    const lm = await LanguageModel.create({
      initialPrompts: [{ role: 'system', content: context + ' You list next steps: concrete things someone agreed or needs to do, with who and when if said. No small talk, no summaries.' }],
    });
    for (const p of parts) {
      const s = await lm.clone();
      const out = await s.prompt('Next steps from this part of the call, as a JSON array of short strings ("Who: what, by when"). [] if none.\n\n' + p,
        { responseConstraint: { type: 'array', items: { type: 'string' } } });
      s.destroy();
      try { steps.push(...JSON.parse(out)); } catch (e) { /* a bad answer just adds nothing */ }
    }
    lm.destroy();
  } else {
    const kp = await Summarizer.create({ type: 'key-points', format: 'plain-text', length: 'short', sharedContext: context + ' List only actions people agreed to take.' });
    for (const p of parts) steps.push(...(await kp.summarize(p)).split('\n').map((s) => s.replace(/^[\s*•-]+/, '')));
    kp.destroy();
  }
  const seen = new Set();
  steps = steps.map((s) => String(s).trim()).filter((s) => s && !seen.has(s.toLowerCase()) && seen.add(s.toLowerCase()));
  return { summary: summary.trim(), next_steps: steps };
}
