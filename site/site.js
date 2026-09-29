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

  // Videos load YouTube only when played (our own thumbnails meanwhile).
  document.querySelectorAll('.play').forEach(b => b.addEventListener('click', () => {
    const f = document.createElement('iframe');
    f.src = `https://www.youtube.com/embed/${b.dataset.id}?autoplay=1&rel=0`;
    f.title = b.getAttribute('aria-label');
    f.allow = 'accelerometer; autoplay; clipboard-write; encrypted-media; gyroscope; picture-in-picture; fullscreen';
    f.allowFullscreen = true;
    b.replaceWith(f);
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
