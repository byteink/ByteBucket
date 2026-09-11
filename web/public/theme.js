// Apply the persisted (or system) theme before first paint to avoid a flash
// of the wrong colors. Lives in its own file because the admin CSP forbids
// inline scripts. Keep the storage key and resolution rule in sync with
// src/lib/theme.ts.
(function () {
  try {
    var t = localStorage.getItem('bytebucket.theme');
    var dark =
      t === 'dark' ||
      ((t === null || t === 'system') &&
        globalThis.matchMedia('(prefers-color-scheme: dark)').matches);
    if (dark) document.documentElement.classList.add('dark');
  } catch (e) {
    // Storage may be blocked (private mode); fall back to the light default.
    console.warn('theme: preference unavailable, using light', e);
  }
})();
