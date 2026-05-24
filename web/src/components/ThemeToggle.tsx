import { useEffect, useRef, useState } from 'react';
import {
  applyTheme,
  loadTheme,
  nextTheme,
  saveTheme,
  watchSystem,
  type Theme,
} from '../lib/theme';

const LABEL: Record<Theme, string> = {
  system: 'System',
  light: 'Light',
  dark: 'Dark',
};

function ThemeIcon({ theme }: { theme: Theme }) {
  const common = {
    width: 14,
    height: 14,
    viewBox: '0 0 24 24',
    fill: 'none',
    stroke: 'currentColor',
    strokeWidth: 2,
    strokeLinecap: 'round' as const,
    strokeLinejoin: 'round' as const,
    'aria-hidden': true,
  };
  if (theme === 'light') {
    return (
      <svg {...common}>
        <circle cx="12" cy="12" r="4" />
        <path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M6.3 17.7l-1.4 1.4M19.1 4.9l-1.4 1.4" />
      </svg>
    );
  }
  if (theme === 'dark') {
    return (
      <svg {...common}>
        <path d="M21 12.8A9 9 0 1 1 11.2 3 7 7 0 0 0 21 12.8z" />
      </svg>
    );
  }
  return (
    <svg {...common}>
      <rect x="2" y="3" width="20" height="14" rx="2" />
      <path d="M8 21h8M12 17v4" />
    </svg>
  );
}

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
      className="btn h-7 px-2 text-xs gap-1.5"
      onClick={onClick}
      title={`Theme: ${LABEL[theme]} (click to change)`}
      aria-label={`Theme: ${LABEL[theme]}. Click to change.`}
    >
      <ThemeIcon theme={theme} />
      {LABEL[theme]}
    </button>
  );
}
