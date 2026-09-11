import { useEffect, useRef, useState } from 'react';
import {
  applyTheme,
  loadTheme,
  nextTheme,
  saveTheme,
  watchSystem,
  type Theme,
} from '../lib/theme';
import { Icon, type IconName } from './icons';

const LABEL: Record<Theme, string> = {
  system: 'System',
  light: 'Light',
  dark: 'Dark',
};

const ICON: Record<Theme, IconName> = {
  system: 'monitor',
  light: 'sun',
  dark: 'moon',
};

// Rotates System -> Light -> Dark on each click. "System" tracks the OS while
// selected; the choice persists across sessions.
export default function ThemeToggle() {
  const [theme, setTheme] = useState<Theme>(loadTheme);
  const themeRef = useRef(theme);
  themeRef.current = theme;

  useEffect(() => {
    applyTheme(theme);
  }, [theme]);

  useEffect(() => watchSystem(() => themeRef.current), []);

  function onClick() {
    const next = nextTheme(theme);
    setTheme(next);
    saveTheme(next);
  }

  return (
    <button
      type="button"
      className="btn-ghost btn-sm flex-1 justify-start gap-2 text-ink-500 hover:text-ink-900 tt"
      onClick={onClick}
      data-tip={`Theme: ${LABEL[theme]}. Click to change.`}
      aria-label={`Theme: ${LABEL[theme]}. Click to change.`}
    >
      <Icon name={ICON[theme]} size={14} />
      {LABEL[theme]}
    </button>
  );
}
