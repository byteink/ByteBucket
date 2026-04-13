import { NavLink, Outlet, useNavigate } from 'react-router-dom';
import { clearSession, loadSession } from '../lib/session';

const navItems = [
  { to: '/buckets', label: 'Buckets' },
  { to: '/users', label: 'Users' },
];

export default function Layout() {
  const navigate = useNavigate();
  const session = loadSession();

  function onLogout() {
    clearSession();
    navigate('/login', { replace: true });
  }

  return (
    <div className="min-h-full flex flex-col">
      <header className="border-b border-ink-200">
        <div className="max-w-5xl mx-auto px-6 h-12 flex items-center justify-between">
          <div className="flex items-center gap-6">
            <span className="font-mono text-sm">ByteBucket</span>
            <nav className="flex items-center gap-4">
              {navItems.map((n) => (
                <NavLink
                  key={n.to}
                  to={n.to}
                  className={({ isActive }) =>
                    `text-sm ${isActive ? 'text-ink-900' : 'text-ink-500 hover:text-ink-900'}`
                  }
                >
                  {n.label}
                </NavLink>
              ))}
            </nav>
          </div>
          <div className="flex items-center gap-3 text-xs text-ink-500">
            <span className="font-mono truncate max-w-[12rem]" title={session?.accessKey}>
              {session?.accessKey}
            </span>
            <button className="btn h-7 px-2 text-xs" onClick={onLogout}>
              Log out
            </button>
          </div>
        </div>
      </header>
      <main className="flex-1">
        <div className="max-w-5xl mx-auto px-6 py-8">
          <Outlet />
        </div>
      </main>
    </div>
  );
}
