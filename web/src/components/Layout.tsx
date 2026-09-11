import { NavLink, Outlet, useNavigate } from 'react-router-dom';
import { clearSession, loadSession } from '../lib/session';
import ThemeToggle from './ThemeToggle';
import { IconButton } from './ui';

const navItems = [
  { to: '/dashboard', label: 'Overview' },
  { to: '/buckets', label: 'Buckets' },
  { to: '/users', label: 'Users' },
  { to: '/logs', label: 'Logs' },
  { to: '/settings', label: 'Settings' },
];

// Layout is the signed-in shell: a fixed left rail with navigation and the
// session, and a fluid main column so tables and logs get the full width.
export default function Layout() {
  const navigate = useNavigate();
  const session = loadSession();

  function onLogout() {
    clearSession();
    navigate('/login', { replace: true });
  }

  return (
    <div className="min-h-full flex">
      <aside className="side">
        <div className="brand">ByteBucket</div>
        <nav aria-label="Main">
          {navItems.map((n) => (
            <NavLink key={n.to} to={n.to}>
              {n.label}
            </NavLink>
          ))}
        </nav>
        <div className="foot">
          <div
            className="flex items-center h-8 px-3 font-mono text-xs text-ink-500 truncate tt tt-right"
            data-tip={`Signed in as ${session?.accessKey ?? ''}`}
          >
            {session?.accessKey}
          </div>
          <div className="flex gap-0.5">
            <ThemeToggle />
            <IconButton icon="logout" label="Log out" onClick={onLogout} />
          </div>
        </div>
      </aside>
      <main className="flex-1 min-w-0 px-10 pt-7 pb-10">
        <Outlet />
      </main>
    </div>
  );
}
