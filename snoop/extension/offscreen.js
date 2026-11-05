chrome.runtime.onMessage.addListener((msg, sender, reply) => {
  if (msg.type !== 'summarise' || msg.target !== 'offscreen') return;
  summarise(msg.lines, msg.title).then((notes) => reply({ notes }), (e) => reply({ error: String(e.message || e) }));
  return true;
});
