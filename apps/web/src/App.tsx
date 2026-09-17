import { Shell } from "./components/Shell";
import { ToastProvider } from "./components/Toast";
import { Link, useLocation } from "./lib/router";
import { AgentsView } from "./views/AgentsView";
import { ApprovalsView } from "./views/ApprovalsView";
import { BoxDetailView } from "./views/BoxDetailView";
import { BoxesView } from "./views/BoxesView";
import { DashboardView } from "./views/DashboardView";
import { HostsView } from "./views/HostsView";
import { MembersView } from "./views/MembersView";
import { WorkspacesView } from "./views/WorkspacesView";

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
  else if (/^\/boxes\/[^/]+$/.test(pathname)) content = <BoxDetailView boxId={decodeURIComponent(pathname.slice("/boxes/".length))} />;
  else content = <NotFound />;

  return <ToastProvider><Shell>{content}</Shell></ToastProvider>;
}

function NotFound() {
  return (
    <div className="not-found">
      <span>404</span>
      <h1>That view doesn’t exist.</h1>
      <p>The address may be out of date, or the resource was removed.</p>
      <Link className="button button--primary" to="/">Return to overview</Link>
    </div>
  );
}
