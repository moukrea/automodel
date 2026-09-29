// Install tabs, copy buttons, and the table of contents' current section.
(() => {
  const tabs = document.querySelectorAll('.tabs button');
  const pick = os => {
    tabs.forEach(b => b.setAttribute('aria-selected', String(b.dataset.os === os)));
    document.querySelectorAll('.cmd').forEach(c => { c.hidden = c.dataset.os !== os; });
  };
  tabs.forEach(b => b.addEventListener('click', () => pick(b.dataset.os)));
  if (/Win/.test(navigator.platform || navigator.userAgent)) pick('win');

  document.addEventListener('click', async e => {
    const btn = e.target.closest('.copy');
    if (!btn) return;
    const box = btn.closest('.cmd, .code');
    const text = box.querySelector('code').innerText;
    try { await navigator.clipboard.writeText(text); } catch { return; }
    btn.textContent = 'Copied'; btn.classList.add('ok');
    setTimeout(() => { btn.textContent = 'Copy'; btn.classList.remove('ok'); }, 1400);
  });

  // The videos are served by the site itself (a YouTube embed asks some
  // viewers to sign in to prove they aren't a bot); nothing loads until
  // played.
  document.querySelectorAll('.play').forEach(b => b.addEventListener('click', () => {
    const v = document.createElement('video');
    v.src = b.dataset.src;
    v.poster = b.querySelector('img').src;
    v.controls = true; v.autoplay = true; v.playsInline = true; v.preload = 'auto';
    v.setAttribute('aria-label', b.getAttribute('aria-label'));
    b.replaceWith(v);
    v.play().catch(() => {});
  }));

  const links = [...document.querySelectorAll('.toc a')];
  const byId = new Map(links.map(a => [a.getAttribute('href').slice(1), a]));
  const heads = [...byId.keys()].map(id => document.getElementById(id)).filter(Boolean);
  if (!('IntersectionObserver' in window) || !heads.length) return;
  const io = new IntersectionObserver(entries => {
    for (const en of entries) if (en.isIntersecting) {
      links.forEach(a => a.classList.remove('on'));
      const a = byId.get(en.target.id);
      if (a) a.classList.add('on');
    }
  }, { rootMargin: '-80px 0px -70% 0px' });
  heads.forEach(h => io.observe(h));
})();
