'use strict';

// Apply the saved (or system) theme before the stylesheet is parsed so the
// first paint already uses the right palette.
(() => {
  const key = 'monitor.theme';
  let saved = null;
  try {
    saved = window.localStorage.getItem(key);
  } catch (_) {
    saved = null;
  }

  const prefersDark = Boolean(
    window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches
  );
  const theme = saved === 'light' || saved === 'dark' ? saved : prefersDark ? 'dark' : 'light';

  document.documentElement.dataset.theme = theme;
  document.documentElement.style.colorScheme = theme;

  const meta = document.querySelector('meta[name="theme-color"]');
  if (meta) {
    meta.setAttribute('content', theme === 'dark' ? '#060a0f' : '#f4f7fb');
  }
})();
