import type { SVGProps } from "react";

export type IconName =
  | "dashboard"
  | "box"
  | "agent"
  | "host"
  | "workspace"
  | "approval"
  | "menu"
  | "close"
  | "plus"
  | "arrow"
  | "refresh"
  | "send"
  | "stop"
  | "interrupt"
  | "resume"
  | "check"
  | "deny"
  | "terminal"
  | "activity"
  | "chevron"
  | "search"
  | "spark"
  | "clock";

interface IconProps extends SVGProps<SVGSVGElement> {
  name: IconName;
  size?: number;
}

export function Icon({ name, size = 18, ...props }: IconProps) {
  const paths: Record<IconName, React.ReactNode> = {
    dashboard: <><rect x="3" y="3" width="7" height="7" rx="2"/><rect x="14" y="3" width="7" height="7" rx="2"/><rect x="3" y="14" width="7" height="7" rx="2"/><rect x="14" y="14" width="7" height="7" rx="2"/></>,
    box: <><path d="m21 8-9-5-9 5 9 5 9-5Z"/><path d="m3 8 9 5 9-5v8l-9 5-9-5V8Z"/></>,
    agent: <><path d="M12 2v3"/><rect x="4" y="5" width="16" height="14" rx="4"/><path d="M8 10h.01M16 10h.01M8 15h8"/></>,
    host: <><rect x="3" y="4" width="18" height="6" rx="2"/><rect x="3" y="14" width="18" height="6" rx="2"/><path d="M7 7h.01M7 17h.01M17 7h1M17 17h1"/></>,
    workspace: <><path d="M3 7h7l2 2h9v10a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V7Z"/><path d="M3 7V5a2 2 0 0 1 2-2h5l2 2h7a2 2 0 0 1 2 2v2"/></>,
    approval: <><path d="M12 3 4 6v6c0 4.7 3.4 7.8 8 9 4.6-1.2 8-4.3 8-9V6l-8-3Z"/><path d="m8.5 12 2.2 2.2 4.8-5"/></>,
    menu: <><path d="M4 6h16M4 12h16M4 18h16"/></>,
    close: <path d="m6 6 12 12M18 6 6 18"/>,
    plus: <path d="M12 5v14M5 12h14"/>,
    arrow: <path d="m9 18 6-6-6-6"/>,
    refresh: <><path d="M20 12a8 8 0 1 1-2.3-5.7L20 8"/><path d="M20 3v5h-5"/></>,
    send: <><path d="m22 2-7 20-4-9-9-4 20-7Z"/><path d="M22 2 11 13"/></>,
    stop: <rect x="6" y="6" width="12" height="12" rx="2"/>,
    interrupt: <><path d="M7 5v14M17 5v14"/></>,
    resume: <path d="m8 5 11 7-11 7V5Z"/>,
    check: <path d="m5 12 4 4L19 6"/>,
    deny: <><circle cx="12" cy="12" r="9"/><path d="m8 8 8 8"/></>,
    terminal: <><path d="m4 7 5 5-5 5M12 17h8"/></>,
    activity: <path d="M3 12h4l2-7 4 14 2-7h6"/>,
    chevron: <path d="m9 18 6-6-6-6"/>,
    search: <><circle cx="11" cy="11" r="7"/><path d="m20 20-4-4"/></>,
    spark: <><path d="m12 3 1.5 4.5L18 9l-4.5 1.5L12 15l-1.5-4.5L6 9l4.5-1.5L12 3Z"/><path d="m19 15 .7 2.3L22 18l-2.3.7L19 21l-.7-2.3L16 18l2.3-.7L19 15Z"/></>,
    clock: <><circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/></>
  };
  return (
    <svg
      aria-hidden="true"
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.8"
      strokeLinecap="round"
      strokeLinejoin="round"
      {...props}
    >
      {paths[name]}
    </svg>
  );
}
