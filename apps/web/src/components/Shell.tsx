import { useEffect, useState, type ReactNode } from "react";
import { api, ApiError } from "../api/client";
import type { CurrentUser, OrganizationRole } from "../api/types";
import { useResource } from "../hooks/useResource";
import { AccessProvider, roleAtLeast } from "../lib/access";
import { initials } from "../lib/format";
import { useI18n } from "../lib/i18n";
import { Link, useLocation } from "../lib/router";
import { Icon, type IconName } from "./Icon";
import { cx } from "./ui";
import { LoginView, PasswordChangeView } from "../views/AuthView";

const NAV_ITEMS: Array<{ to: string; labelKey: string; icon: IconName; minimumRole?: OrganizationRole }> = [
  { to: "/", labelKey: "nav.overview", icon: "dashboard" },
  { to: "/agents", labelKey: "nav.agents", icon: "agent" },
  { to: "/boxes", labelKey: "nav.boxes", icon: "box" },
  { to: "/schedules", labelKey: "nav.schedules", icon: "schedule" },
  { to: "/approvals", labelKey: "nav.approvals", icon: "approval", minimumRole: "user" },
  { to: "/notifications", labelKey: "nav.notifications", icon: "notification" },
  { to: "/hosts", labelKey: "nav.hosts", icon: "host" },
  { to: "/workspaces", labelKey: "nav.workspaces", icon: "workspace" },
  { to: "/members", labelKey: "nav.organization", icon: "members", minimumRole: "admin" }
];

export function Shell({ children }: { children: ReactNode }) {
  const location = useLocation();
  const { locale, setLocale, t } = useI18n();
  const [mobileOpen, setMobileOpen] = useState(false);
  const meta = useResource((signal) => api.getMeta(signal), []);
  const authenticated = Boolean(meta.data);
  const canReviewApprovals = roleAtLeast(meta.data?.currentUser.role, "user");
  const approvals = useResource((signal) => authenticated && canReviewApprovals ? api.listApprovals(signal) : Promise.resolve([]), [authenticated, canReviewApprovals]);
  const notifications = useResource((signal) => authenticated ? api.listNotifications(signal) : Promise.resolve([]), [authenticated]);

  useEffect(() => setMobileOpen(false), [location.pathname]);
  useEffect(() => {
    const interval = window.setInterval(() => {
      if (!authenticated) return;
      notifications.reload();
      if (canReviewApprovals) approvals.reload();
    }, 30_000);
    return () => window.clearInterval(interval);
  }, [approvals.reload, authenticated, canReviewApprovals, notifications.reload]);
  useEffect(() => {
    const refreshNotifications = () => notifications.reload();
    const refreshApprovals = () => approvals.reload();
    window.addEventListener("agentbox:notifications-changed", refreshNotifications);
    window.addEventListener("agentbox:approvals-changed", refreshApprovals);
    return () => {
      window.removeEventListener("agentbox:notifications-changed", refreshNotifications);
      window.removeEventListener("agentbox:approvals-changed", refreshApprovals);
    };
  }, [approvals.reload, notifications.reload]);

  const pendingApprovals = approvals.data?.filter((approval) => approval.status === "pending").length ?? 0;
  const unreadNotifications = notifications.data?.filter((notification) => notification.status === "unread").length ?? 0;
  const pageTitle = /^\/boxes\/[^/]+\/(subagents|todos|artifacts|diff)$/.test(location.pathname)
    ? t("shell.boxOutput")
    : location.pathname.startsWith("/boxes/")
      ? t("shell.boxSession")
      : t(NAV_ITEMS.find((item) => item.to === location.pathname)?.labelKey ?? "nav.overview");

  const visibleNavItems = NAV_ITEMS.filter((item) => !item.minimumRole || roleAtLeast(meta.data?.currentUser.role, item.minimumRole));
  const currentUser = meta.data?.currentUser;

  const logout = async () => {
    try {
      await api.logout();
    } finally {
      meta.setData(undefined);
      meta.reload();
    }
  };

  if (meta.loading && !meta.data) {
    return <div className="auth-screen"><div className="auth-card auth-card--loading"><span className="spinner" /><p>Checking account session…</p></div></div>;
  }
  if (meta.error instanceof ApiError && meta.error.status === 401) {
    return <LoginView onAuthenticated={() => meta.reload()} />;
  }
  if (currentUser?.mustChangePassword) {
    return <PasswordChangeView user={currentUser} onChanged={(user: CurrentUser) => meta.setData((current) => current ? { ...current, currentUser: user } : current)} onLogout={() => void logout()} />;
  }

  return (
    <AccessProvider meta={meta.data} loading={meta.loading} error={meta.error}>
      <div className="app-shell">
        <a className="skip-link" href="#main-content">{t("shell.skip")}</a>
        <aside className={cx("sidebar", mobileOpen && "sidebar--open")} aria-label={t("shell.primaryNav")}>
          <div className="brand">
            <span className="brand__mark" aria-hidden="true"><Icon name="box" size={23} /></span>
            <div><strong>AgentBox</strong><span>{t("shell.console")}</span></div>
          </div>
          <nav className="primary-nav" id="primary-navigation">
            {visibleNavItems.map((item) => {
              const active = item.to === "/" ? location.pathname === "/" : location.pathname.startsWith(item.to);
              return (
                <Link className={cx("primary-nav__item", active && "primary-nav__item--active")} to={item.to} key={item.to} aria-current={active ? "page" : undefined}>
                  <Icon name={item.icon} />
                  <span>{t(item.labelKey)}</span>
                  {item.to === "/approvals" && pendingApprovals > 0 ? <span className="nav-badge" aria-label={`${pendingApprovals} pending`}>{pendingApprovals}</span> : null}
                  {item.to === "/notifications" && unreadNotifications > 0 ? <span className="nav-badge nav-badge--notification" aria-label={`${unreadNotifications} unread`}>{unreadNotifications}</span> : null}
                </Link>
              );
            })}
          </nav>
          <div className="sidebar__footer">
            {currentUser ? <div className="sidebar-user-row">{roleAtLeast(currentUser.role, "admin") ? <Link className="sidebar-user" to="/members"><span className="member-avatar member-avatar--small">{initials(currentUser.displayName || currentUser.login)}</span><span><strong>{currentUser.displayName || currentUser.login}</strong><small>{currentUser.role} · {currentUser.login}</small></span></Link> : <div className="sidebar-user"><span className="member-avatar member-avatar--small">{initials(currentUser.displayName || currentUser.login)}</span><span><strong>{currentUser.displayName || currentUser.login}</strong><small>{currentUser.role} · {currentUser.login}</small></span></div>}<button className="icon-button" type="button" onClick={() => void logout()} aria-label="Sign out" title="Sign out"><Icon name="close" /></button></div> : null}
            <label className="language-switch"><span>Language / 语言</span><select value={locale} onChange={(event) => setLocale(event.target.value as "zh-CN" | "en")}><option value="zh-CN">{t("language.zh")}</option><option value="en">{t("language.en")}</option></select></label>
            <div className={cx("server-state", meta.error ? "server-state--error" : "server-state--online")}>
              <span aria-hidden="true" />
              <div>
                <strong>{meta.error ? t("shell.serverUnavailable") : t("shell.serverConnected")}</strong>
                <small>{meta.data ? `API ${meta.data.apiVersion} · ${meta.data.serverVersion}` : meta.loading ? t("shell.checking") : t("shell.retry")}</small>
              </div>
            </div>
          </div>
        </aside>
        {mobileOpen ? <button className="nav-scrim" aria-label={t("shell.closeNav")} onClick={() => setMobileOpen(false)} /> : null}
        <div className="app-frame">
          <header className="mobile-header">
            <button className="icon-button" type="button" onClick={() => setMobileOpen(true)} aria-expanded={mobileOpen} aria-controls="primary-navigation" aria-label={t("shell.openNav")}>
              <Icon name="menu" />
            </button>
            <span>{pageTitle}</span>
            <span className={cx("connection-dot", Boolean(meta.error) && "connection-dot--error")} title={meta.error ? t("shell.serverUnavailable") : t("shell.serverConnected")} />
          </header>
          <main id="main-content" className="main-content" tabIndex={-1}>{children}</main>
        </div>
      </div>
    </AccessProvider>
  );
}
