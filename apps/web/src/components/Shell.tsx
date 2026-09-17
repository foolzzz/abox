import { useEffect, useState, type ReactNode } from "react";
import { api } from "../api/client";
import type { OrganizationRole } from "../api/types";
import { useResource } from "../hooks/useResource";
import { AccessProvider, roleAtLeast } from "../lib/access";
import { initials } from "../lib/format";
import { Link, useLocation } from "../lib/router";
import { Icon, type IconName } from "./Icon";
import { cx } from "./ui";

const NAV_ITEMS: Array<{ to: string; label: string; icon: IconName; minimumRole?: OrganizationRole }> = [
  { to: "/", label: "Overview", icon: "dashboard" },
  { to: "/boxes", label: "Boxes", icon: "box" },
  { to: "/approvals", label: "Approvals", icon: "approval", minimumRole: "operator" },
  { to: "/agents", label: "Agents", icon: "agent" },
  { to: "/hosts", label: "Hosts", icon: "host" },
  { to: "/workspaces", label: "Workspaces", icon: "workspace" },
  { to: "/members", label: "Organization", icon: "members" }
];

export function Shell({ children }: { children: ReactNode }) {
  const location = useLocation();
  const [mobileOpen, setMobileOpen] = useState(false);
  const meta = useResource((signal) => api.getMeta(signal), []);
  const canReviewApprovals = roleAtLeast(meta.data?.currentUser.role, "operator");
  const approvals = useResource((signal) => canReviewApprovals ? api.listApprovals(signal) : Promise.resolve([]), [canReviewApprovals]);

  useEffect(() => setMobileOpen(false), [location.pathname]);
  useEffect(() => {
    if (!canReviewApprovals) return;
    const interval = window.setInterval(approvals.reload, 30_000);
    return () => window.clearInterval(interval);
  }, [approvals.reload, canReviewApprovals]);

  const pendingApprovals = approvals.data?.filter((approval) => approval.status === "pending").length ?? 0;
  const pageTitle = location.pathname.startsWith("/boxes/")
    ? "Box session"
    : NAV_ITEMS.find((item) => item.to === location.pathname)?.label ?? "AgentBox";

  const visibleNavItems = NAV_ITEMS.filter((item) => !item.minimumRole || roleAtLeast(meta.data?.currentUser.role, item.minimumRole));
  const currentUser = meta.data?.currentUser;

  return (
    <AccessProvider meta={meta.data} loading={meta.loading} error={meta.error}>
      <div className="app-shell">
        <a className="skip-link" href="#main-content">Skip to content</a>
        <aside className={cx("sidebar", mobileOpen && "sidebar--open")} aria-label="Primary navigation">
          <div className="brand">
            <span className="brand__mark" aria-hidden="true"><Icon name="box" size={23} /></span>
            <div><strong>AgentBox</strong><span>Operations console</span></div>
          </div>
          <nav className="primary-nav" id="primary-navigation">
            {visibleNavItems.map((item) => {
              const active = item.to === "/" ? location.pathname === "/" : location.pathname.startsWith(item.to);
              return (
                <Link className={cx("primary-nav__item", active && "primary-nav__item--active")} to={item.to} key={item.to} aria-current={active ? "page" : undefined}>
                  <Icon name={item.icon} />
                  <span>{item.label}</span>
                  {item.to === "/approvals" && pendingApprovals > 0 ? <span className="nav-badge" aria-label={`${pendingApprovals} pending`}>{pendingApprovals}</span> : null}
                </Link>
              );
            })}
          </nav>
          <div className="sidebar__footer">
            {currentUser ? <Link className="sidebar-user" to="/members"><span className="member-avatar member-avatar--small">{initials(currentUser.displayName || currentUser.login)}</span><span><strong>{currentUser.displayName || currentUser.login}</strong><small>{currentUser.role} · {currentUser.login}</small></span></Link> : null}
            <div className={cx("server-state", meta.error ? "server-state--error" : "server-state--online")}>
              <span aria-hidden="true" />
              <div>
                <strong>{meta.error ? "Server unavailable" : "Server connected"}</strong>
                <small>{meta.data ? `API ${meta.data.apiVersion} · ${meta.data.serverVersion}` : meta.loading ? "Checking connection" : "Retry from any view"}</small>
              </div>
            </div>
          </div>
        </aside>
        {mobileOpen ? <button className="nav-scrim" aria-label="Close navigation" onClick={() => setMobileOpen(false)} /> : null}
        <div className="app-frame">
          <header className="mobile-header">
            <button className="icon-button" type="button" onClick={() => setMobileOpen(true)} aria-expanded={mobileOpen} aria-controls="primary-navigation" aria-label="Open navigation">
              <Icon name="menu" />
            </button>
            <span>{pageTitle}</span>
            <span className={cx("connection-dot", Boolean(meta.error) && "connection-dot--error")} title={meta.error ? "Server unavailable" : "Server connected"} />
          </header>
          <main id="main-content" className="main-content" tabIndex={-1}>{children}</main>
        </div>
      </div>
    </AccessProvider>
  );
}
