import { Shell } from "./components/Shell";
import { ToastProvider } from "./components/Toast";
import { Link, useLocation } from "./lib/router";
import { useI18n } from "./lib/i18n";
import { AgentsView } from "./views/AgentsView";
import { ApprovalsView } from "./views/ApprovalsView";
import { BoxAutomationView, type BoxAutomationPanel } from "./views/BoxAutomationView";
import { BoxDetailView } from "./views/BoxDetailView";
import { BoxesView } from "./views/BoxesView";
import { DashboardView } from "./views/DashboardView";
import { HostsView } from "./views/HostsView";
import { MembersView } from "./views/MembersView";
import { NotificationsView } from "./views/NotificationsView";
import { SchedulesView } from "./views/SchedulesView";
import { WorkspacesView } from "./views/WorkspacesView";
import { TerminalView } from "./views/TerminalView";

export function App() {
  const { pathname } = useLocation();
  let content: React.ReactNode;

  if (pathname === "/") content = <DashboardView />;
  else if (pathname === "/agents") content = <AgentsView />;
  else if (pathname === "/hosts") content = <HostsView />;
  else if (pathname === "/members") content = <MembersView />;
  else if (pathname === "/workspaces") content = <WorkspacesView />;
  else if (pathname === "/boxes") content = <BoxesView />;
  else if (pathname === "/approvals") content = <ApprovalsView />;
  else if (pathname === "/schedules") content = <SchedulesView />;
  else if (pathname === "/notifications") content = <NotificationsView />;
  else if (/^\/boxes\/[^/]+\/(agent-terminal|command-terminal|terminal)$/.test(pathname)) {
    const [, boxId, terminalKind] = pathname.match(/^\/boxes\/([^/]+)\/(agent-terminal|command-terminal|terminal)$/)!;
    content = <TerminalView boxId={decodeURIComponent(boxId!)} mode={terminalKind === "agent-terminal" ? "agent" : "command"} />;
  }
  else if (/^\/boxes\/[^/]+\/(subagents|todos|artifacts|diff)$/.test(pathname)) {
    const [, boxId, panel] = pathname.match(/^\/boxes\/([^/]+)\/(subagents|todos|artifacts|diff)$/)!;
    content = <BoxAutomationView boxId={decodeURIComponent(boxId!)} panel={panel as BoxAutomationPanel} />;
  }
  else if (/^\/boxes\/[^/]+$/.test(pathname)) content = <BoxDetailView boxId={decodeURIComponent(pathname.slice("/boxes/".length))} />;
  else content = <NotFound />;

  return <ToastProvider><Shell>{content}</Shell></ToastProvider>;
}

function NotFound() {
  const { t } = useI18n();
  return (
    <div className="not-found">
      <span>404</span>
      <h1>{t("notFound.title")}</h1>
      <p>{t("notFound.description")}</p>
      <Link className="button button--primary" to="/">{t("notFound.return")}</Link>
    </div>
  );
}
