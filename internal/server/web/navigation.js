/* Navigation owns only its drawer; app.js owns media/detail overlays. */
(() => {
  'use strict';
  const drawer = document.getElementById('nav-drawer');
  const toggle = document.getElementById('nav-toggle');
  const close = document.getElementById('nav-close');
  const backdrop = document.getElementById('nav-backdrop');
  const main = document.getElementById('main-content');
  if (!drawer || !toggle || !close || !backdrop || !main) return;
  const mobile = window.matchMedia('(max-width: 860px)');
  const overlays = ['detail-overlay', 'player-overlay', 'scrape-overlay'];
  let opened = false;
  let previousFocus = null;
  let previousInert = false;
  const visibleOverlay = () => overlays.some(id => {
    const node = document.getElementById(id);
    return node && !node.hidden;
  });
  const focusables = () => Array.from(drawer.querySelectorAll('button:not([disabled]), a[href], [tabindex="0"]'))
    .filter(node => !node.hidden && node.getClientRects().length);
  function setOpen(next, restore = true) {
    next = !!next && mobile.matches && !visibleOverlay();
    if (next === opened) return;
    opened = next;
    document.body.classList.toggle('nav-open', next);
    toggle.setAttribute('aria-expanded', String(next));
    backdrop.hidden = !next;
    if (next) {
      previousFocus = document.activeElement;
      previousInert = main.inert;
      drawer.inert = false;
      drawer.removeAttribute('aria-hidden');
      drawer.setAttribute('role', 'dialog');
      drawer.setAttribute('aria-modal', 'true');
      main.inert = true;
      (focusables()[0] || drawer).focus();
    } else {
      main.inert = previousInert;
      drawer.removeAttribute('role');
      drawer.removeAttribute('aria-modal');
      if (restore && previousFocus && previousFocus.isConnected) previousFocus.focus();
      drawer.inert = mobile.matches;
      if (mobile.matches) drawer.setAttribute('aria-hidden', 'true');
      else drawer.removeAttribute('aria-hidden');
      previousFocus = null;
    }
  }
  function reset() {
    const focusedInDrawer = drawer.contains(document.activeElement);
    setOpen(false, mobile.matches);
    drawer.inert = mobile.matches;
    if (mobile.matches) {
      drawer.setAttribute('aria-hidden', 'true');
      if (focusedInDrawer) toggle.focus();
    } else {
      drawer.removeAttribute('aria-hidden');
      if (document.activeElement === toggle || document.activeElement === close) {
        const firstNavigation = drawer.querySelector('.nav-item');
        (firstNavigation || focusables()[0] || drawer).focus();
      }
    }
  }
  toggle.addEventListener('click', () => setOpen(!opened));
  close.addEventListener('click', () => setOpen(false));
  backdrop.addEventListener('click', () => setOpen(false));
  drawer.addEventListener('click', event => {
    if (event.target.closest('.nav-item')) setOpen(false);
  });
  document.addEventListener('keydown', event => {
    if (!opened) return;
    if (event.key === 'Escape') {
      event.preventDefault();
      event.stopImmediatePropagation();
      setOpen(false);
    } else if (event.key === 'Tab') {
      const nodes = focusables();
      const first = nodes[0] || drawer;
      const last = nodes[nodes.length - 1] || drawer;
      if (event.shiftKey && (document.activeElement === first || !drawer.contains(document.activeElement))) {
        event.preventDefault(); last.focus();
      } else if (!event.shiftKey && (document.activeElement === last || !drawer.contains(document.activeElement))) {
        event.preventDefault(); first.focus();
      }
    }
  }, true);
  mobile.addEventListener('change', reset);
  window.addEventListener('hashchange', () => setOpen(false));
  const observer = new MutationObserver(() => {
    if (opened && visibleOverlay()) setOpen(false, false);
    drawer.querySelectorAll('.nav-group').forEach(group => {
      group.hidden = !group.querySelector('.nav-item');
    });
  });
  overlays.forEach(id => {
    const node = document.getElementById(id);
    if (node) observer.observe(node, { attributes: true, attributeFilter: ['hidden'] });
  });
  observer.observe(drawer, { childList: true, subtree: true });
  const scanTools = document.querySelector('.scan-tools');
  if (scanTools) {
    document.addEventListener('click', event => {
      if (!scanTools.contains(event.target)) scanTools.open = false;
    });
    scanTools.addEventListener('keydown', event => {
      if (event.key === 'Escape' && scanTools.open) {
        event.stopPropagation();
        scanTools.open = false;
        scanTools.querySelector('summary').focus();
      }
    });
  }
  reset();
})();
