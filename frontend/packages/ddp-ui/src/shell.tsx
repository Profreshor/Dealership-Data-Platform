import type { ReactNode } from "react";
import type { Portal, Session } from "./api";
import { Brand } from "./brand";

export function AppShell({ portal, session, activePath, onLogout, logoutPending, logo, children }: {
  portal: Portal;
  session: Session;
  activePath: string;
  onLogout: () => void;
  logoutPending: boolean;
  logo?: string | null;
  children: ReactNode;
}) {
  return <div className="app-shell">
    <header className="app-header">
      <a className="brand" href="/" aria-label={`${portal.display_name} home`}><Brand displayName={portal.display_name} logo={logo ?? null} /></a>
      <details className="account-menu">
        <summary aria-label="Account menu"><span className="avatar" aria-hidden="true">{session.user.email.slice(0, 1).toUpperCase()}</span><span className="account-copy"><strong>{session.user.email}</strong><small>{session.user.admin ? "Administrator" : "Member"}</small></span></summary>
        <div className="account-links"><a href="/profile" aria-current={activePath === "/profile" ? "page" : undefined}>Profile</a>{(session.user.admin || session.user.permissions.includes("users.manage")) && <a href="/admin/users" aria-current={activePath === "/admin/users" ? "page" : undefined}>Users &amp; roles</a>}<button type="button" onClick={onLogout} disabled={logoutPending}>{logoutPending ? "Signing out…" : "Sign out"}</button></div>
      </details>
    </header>
    <div className="app-frame">
      <nav className="side-nav" aria-label="Workspace navigation">
        {portal.pages.map((page) => <a key={page.path} href={page.path} aria-current={page.path === activePath ? "page" : undefined}>{page.label}</a>)}
      </nav>
      <main className="workspace">{children}</main>
    </div>
  </div>;
}
