// Theme preference: "system" follows the OS, "light"/"dark" pin a choice.
// The actual flip is just the `dark` class on <html>; every ink-* token is
// remapped under html.dark in styles.css. The boot snippet in index.html
// applies the same logic before first paint, so keep the storage key and the
// resolution rule in sync with it.
export type Theme = 'system' | 'light' | 'dark';

const STORAGE_KEY = 'bytebucket.theme';
const ORDER: readonly Theme[] = ['system', 'light', 'dark'];

function systemPrefersDark(): boolean {
  return globalThis.matchMedia('(prefers-color-scheme: dark)').matches;
}

export function loadTheme(): Theme {
  try {
    const v = localStorage.getItem(STORAGE_KEY);
    if (v === 'light' || v === 'dark' || v === 'system') return v;
  } catch (e) {
    console.warn('theme: read failed, using system', e);
  }
  return 'system';
}

export function applyTheme(theme: Theme): void {
  const dark = theme === 'dark' || (theme === 'system' && systemPrefersDark());
  document.documentElement.classList.toggle('dark', dark);
}

export function saveTheme(theme: Theme): void {
  try {
    localStorage.setItem(STORAGE_KEY, theme);
  } catch (e) {
    console.warn('theme: persist failed, applying for this session only', e);
  }
  applyTheme(theme);
}

export function nextTheme(theme: Theme): Theme {
  return ORDER[(ORDER.indexOf(theme) + 1) % ORDER.length];
}

// Re-apply on OS change, but only while the user is on "system".
// Returns an unsubscribe function for effect cleanup.
export function watchSystem(getTheme: () => Theme): () => void {
  const mq = globalThis.matchMedia('(prefers-color-scheme: dark)');
  const onChange = (): void => {
    if (getTheme() === 'system') applyTheme('system');
  };
  mq.addEventListener('change', onChange);
  return () => mq.removeEventListener('change', onChange);
}
