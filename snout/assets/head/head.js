// The badger head follows the pointer while you hover it (Marek 2026-11-09):
// the nose sits under the pointer, in 21 x 21 hand-drawn-style frames built
// from nine key poses (tools/badger-head). The orange corners never move.
//
// Markup: <span class="bhead" data-bhead="small|large" [data-corners=URL]
// [data-blink]><img src="the static head"></span>. The static image stays
// until the frames have loaded (and is all you get without JS). small =
// one sprite sheet (the header heads); large = one file per frame (the
// hub), loaded in the background. With data-blink the eyelids blink and
// wink at random, wherever the eyes are in the current frame.
(function () {
  if (window.__bhead) return; // the script may be included more than once
  window.__bhead = true;
  var reduce = matchMedia('(prefers-reduced-motion: reduce)').matches;
  var css = document.createElement('style');
  css.textContent =
    '.bhead{position:relative;display:inline-block;line-height:0}' +
    '.bhead>.bh-face,.bhead>.bh-corners{position:absolute;inset:0;width:100%;height:100%;background-repeat:no-repeat;pointer-events:none;display:none}' +
    '.bhead>.bh-corners{background-size:100% 100%}' +
    '.bhead.live>img:not(.bh-face){visibility:hidden}.bhead.live>.bh-face,.bhead.live>.bh-corners{display:block}' +
    '.bhead .bh-lid{position:absolute;border-radius:50%;overflow:hidden;transform:translate(-50%,-50%);pointer-events:none}' +
    '.bhead .bh-lid::before{content:"";position:absolute;inset:0;border-radius:50%;background:#0f0f0f;transform:translateY(-101%)}' +
    '.bhead .bh-lid.blink::before{animation:bh-shut .34s ease-in-out}.bhead .bh-lid.wink::before{animation:bh-wink .75s ease-in-out}' +
    '@keyframes bh-shut{0%,100%{transform:translateY(-101%)}45%,55%{transform:translateY(0)}}' +
    '@keyframes bh-wink{0%,100%{transform:translateY(-101%)}22%,72%{transform:translateY(0)}}';
  document.head.appendChild(css);

  var metas = {};
  function meta(name) {
    return metas[name] || (metas[name] = fetch('/assets/head/' + name + '.json').then(function (r) { if (!r.ok) throw r; return r.json(); }));
  }
  function pad(n) { return (n < 10 ? '0' : '') + n; }

  function setup(el) {
    var name = el.dataset.bhead;
    meta(name).then(function (m) {
      if (m.sheet) {
        var sheet = new Image();
        sheet.onload = function () { start(el, m, null); };
        sheet.src = '/assets/head/' + m.sheet;
      } else {
        start(el, m, function (i, j) { return '/assets/head/' + m.frames.replace('{i}', pad(i)).replace('{j}', pad(j)); });
      }
    }).catch(function () { /* keep the static head */ });
  }

  function start(el, m, frameURL) {
    var face;
    var loaded = {};
    if (frameURL) {
      face = document.createElement('img'); face.alt = ''; face.className = 'bh-face';
      // the frames nearest the resting logo first, then the rest in the background
      var order = [];
      for (var i = 0; i < m.rows; i++) for (var j = 0; j < m.cols; j++) order.push([i, j]);
      order.sort(function (a, b) {
        return Math.hypot(a[0] - m.rest[0], a[1] - m.rest[1]) - Math.hypot(b[0] - m.rest[0], b[1] - m.rest[1]);
      });
      var next = 0;
      (function load() {
        for (var n = 0; n < 6 && next < order.length; n++, next++) {
          (function (f) { var im = new Image(); im.onload = function () { loaded[f[0] + '_' + f[1]] = im.src; }; im.src = frameURL(f[0], f[1]); })(order[next]);
        }
        if (next < order.length) setTimeout(load, 40);
      })();
    } else {
      face = document.createElement('span'); face.className = 'bh-face';
      face.style.backgroundImage = 'url(/assets/head/' + m.sheet + ')';
    }
    el.appendChild(face);
    if (el.dataset.corners) {
      var c = document.createElement('span'); c.className = 'bh-corners';
      c.style.backgroundImage = 'url(' + el.dataset.corners + ')';
      el.appendChild(c);
    }
    var lids = [];
    if (el.hasAttribute('data-blink')) {
      for (var k = 0; k < 2; k++) { var l = document.createElement('span'); l.className = 'bh-lid'; el.appendChild(l); lids.push(l); }
    }
    var restU = 0.5, restV = 1.0;
    var h = { u: restU, v: restV, tu: restU, tv: restV }, running = false, shown = '';

    function cell(t, n) {   // the pointer from..to across the head -> frame 0..n-1
      return Math.round(Math.max(0, Math.min(1, (t - m.from) / (m.to - m.from))) * (n - 1));
    }
    function nearestLoaded(i, j) {
      if (loaded[i + '_' + j]) return [i, j];
      var best = null, bd = 1e9;
      for (var key in loaded) {
        var p = key.split('_'), d = Math.hypot(p[0] - i, p[1] - j);
        if (d < bd) { bd = d; best = [+p[0], +p[1]]; }
      }
      return best;
    }
    function draw() {
      var W = el.clientWidth, H = el.clientHeight;
      var i = cell(h.v, m.rows), j = cell(h.u, m.cols);
      if (frameURL) {
        var f = nearestLoaded(i, j);
        if (!f) return;
        i = f[0]; j = f[1];
        var s = loaded[i + '_' + j];
        if (s !== shown) { shown = s; face.src = s; }
      } else {
        face.style.backgroundSize = (m.cols * W) + 'px ' + (m.rows * H) + 'px';
        face.style.backgroundPosition = (-j * W) + 'px ' + (-i * H) + 'px';
      }
      (m.eyes[i * m.cols + j] || []).forEach(function (e, n) {
        var l = lids[n];
        if (!l) return;
        l.style.left = e[0] * W + 'px'; l.style.top = e[1] * H + 'px';
        l.style.width = Math.max(2, e[2] * W) + 'px'; l.style.height = Math.max(2, e[3] * H) + 'px';
        l.style.display = e[4] ? '' : 'none'; // an eye out of sight doesn't blink
      });
    }
    function tick() {
      var k = reduce ? 1 : 0.16;
      h.u += (h.tu - h.u) * k; h.v += (h.tv - h.v) * k;
      var moving = Math.abs(h.tu - h.u) > 0.002 || Math.abs(h.tv - h.v) > 0.002;
      if (!moving) { h.u = h.tu; h.v = h.tv; }
      draw();
      running = moving;
      if (moving) requestAnimationFrame(tick);
    }
    function kick() { if (!running) { running = true; requestAnimationFrame(tick); } }
    // the nose goes to the pointer, wherever it is over the head
    el.addEventListener('pointermove', function (e) {
      var b = el.getBoundingClientRect();
      h.tu = (e.clientX - b.left) / b.width; h.tv = (e.clientY - b.top) / b.height;
      kick();
    });
    el.addEventListener('pointerleave', function () { h.tu = restU; h.tv = restV; kick(); });
    addEventListener('resize', draw);
    if (frameURL) {
      // show once the resting frame is in
      (function wait() { if (loaded[m.rest[0] + '_' + m.rest[1]]) { draw(); el.classList.add('live'); } else setTimeout(wait, 50); })();
    } else {
      draw();
      el.classList.add('live');
    }

    if (lids.length && !reduce) {
      var play = function (els, cls) {
        els.forEach(function (l) { l.classList.remove('blink', 'wink'); void l.offsetWidth; l.classList.add(cls); });
      };
      (function nextBlink() {
        setTimeout(function () {
          var r = Math.random();
          if (r < 0.2) play([lids[Math.random() < 0.5 ? 0 : 1]], 'wink');
          else { play(lids, 'blink'); if (r > 0.85) setTimeout(function () { play(lids, 'blink'); }, 420); }
          nextBlink();
        }, 2500 + Math.random() * 4500);
      })();
    }
  }

  function all() { document.querySelectorAll('.bhead[data-bhead]:not([data-ready])').forEach(function (el) { el.setAttribute('data-ready', ''); setup(el); }); }
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', all); else all();
})();
