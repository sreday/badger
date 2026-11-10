// The badger head turns to look at the pointer while you hover it
// (Marek 2026-11-09). 121 rendered views of the logo's head (11 left-right
// x 11 up-down, small.webp / large.webp with their .json: angles and where
// the eyes are in each view); the orange corners never move.
//
// Markup: <span class="bhead" data-bhead="small|large" [data-corners=URL]
// [data-blink]><img src="the static head"></span>. The static image stays
// until the views have loaded (and is all you get without JS). With
// data-blink the eyelids blink and wink at random, wherever the eyes are.
(function () {
  if (window.__bhead) return; // the script may be included more than once
  window.__bhead = true;
  var reduce = matchMedia('(prefers-reduced-motion: reduce)').matches;
  var css = document.createElement('style');
  css.textContent =
    '.bhead{position:relative;display:inline-block;line-height:0}' +
    '.bhead>.bh-face,.bhead>.bh-corners{position:absolute;inset:0;background-repeat:no-repeat;pointer-events:none;display:none}' +
    '.bhead>.bh-corners{background-size:100% 100%}' +
    '.bhead.live>img{visibility:hidden}.bhead.live>.bh-face,.bhead.live>.bh-corners{display:block}' +
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

  function setup(el) {
    var name = el.dataset.bhead, img = el.querySelector('img');
    meta(name).then(function (m) {
      var sprite = new Image();
      sprite.onload = function () { start(el, img, m, '/assets/head/' + name + '.webp'); };
      sprite.src = '/assets/head/' + name + '.webp';
    }).catch(function () { /* keep the static head */ });
  }

  function start(el, img, m, url) {
    var face = document.createElement('span'); face.className = 'bh-face';
    face.style.backgroundImage = 'url(' + url + ')';
    el.appendChild(face);
    if (el.dataset.corners) {
      var c = document.createElement('span'); c.className = 'bh-corners';
      c.style.backgroundImage = 'url(' + el.dataset.corners + ')';
      el.appendChild(c);
    }
    var lids = [];
    if (el.hasAttribute('data-blink')) {
      for (var i = 0; i < 2; i++) { var l = document.createElement('span'); l.className = 'bh-lid'; el.appendChild(l); lids.push(l); }
    }
    var YMAX = m.yaws[m.yaws.length - 1], PUP = m.pitches[0], PDN = -m.pitches[m.pitches.length - 1];
    var h = { yaw: 0, pitch: 0, ty: 0, tp: 0 }, running = false;

    function draw() {
      var W = el.clientWidth, H = el.clientHeight;
      var c = Math.round((h.yaw + YMAX) / (2 * YMAX) * (m.cols - 1));
      var r = Math.round((PUP - h.pitch) / (PUP + PDN) * (m.rows - 1));
      c = Math.max(0, Math.min(m.cols - 1, c)); r = Math.max(0, Math.min(m.rows - 1, r));
      face.style.backgroundSize = (m.cols * W) + 'px ' + (m.rows * H) + 'px';
      face.style.backgroundPosition = (-c * W) + 'px ' + (-r * H) + 'px';
      var sx = W / m.fw, sy = H / m.fh;
      m.eyes[r * m.cols + c].forEach(function (e, k) {
        var l = lids[k];
        if (!l) return;
        l.style.left = e[0] * sx + 'px'; l.style.top = e[1] * sy + 'px';
        l.style.width = Math.max(2, e[2] * sx) + 'px'; l.style.height = Math.max(2, e[3] * sy) + 'px';
        l.style.display = e[4] ? '' : 'none'; // an eye turned out of sight doesn't blink
      });
    }
    function tick() {
      var k = reduce ? 1 : 0.16;
      h.yaw += (h.ty - h.yaw) * k; h.pitch += (h.tp - h.pitch) * k;
      var moving = Math.abs(h.ty - h.yaw) > 0.05 || Math.abs(h.tp - h.pitch) > 0.05;
      if (!moving) { h.yaw = h.ty; h.pitch = h.tp; }
      draw();
      running = moving;
      if (moving) requestAnimationFrame(tick);
    }
    function kick() { if (!running) { running = true; requestAnimationFrame(tick); } }
    // the pointer across the head (inside the corners) -> where the nose points
    el.addEventListener('pointermove', function (e) {
      var b = el.getBoundingClientRect();
      var u = Math.max(-1, Math.min(1, (e.clientX - b.left) / b.width * 2 - 1));
      var v = Math.max(-1, Math.min(1, (e.clientY - b.top) / b.height * 2 - 1));
      h.ty = u * YMAX;
      h.tp = v < 0 ? -v * PUP : -v * PDN;
      kick();
    });
    el.addEventListener('pointerleave', function () { h.ty = 0; h.tp = 0; kick(); });
    addEventListener('resize', draw);
    draw();
    el.classList.add('live');

    if (lids.length && !reduce) {
      var play = function (els, cls) {
        els.forEach(function (l) { l.classList.remove('blink', 'wink'); void l.offsetWidth; l.classList.add(cls); });
      };
      (function next() {
        setTimeout(function () {
          var r = Math.random();
          if (r < 0.2) play([lids[Math.random() < 0.5 ? 0 : 1]], 'wink');
          else { play(lids, 'blink'); if (r > 0.85) setTimeout(function () { play(lids, 'blink'); }, 420); }
          next();
        }, 2500 + Math.random() * 4500);
      })();
    }
  }

  function all() { document.querySelectorAll('.bhead[data-bhead]:not([data-ready])').forEach(function (el) { el.setAttribute('data-ready', ''); setup(el); }); }
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', all); else all();
})();
