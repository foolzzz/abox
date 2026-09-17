import { useEffect, useRef, type ButtonHTMLAttributes, type FormEvent, type ReactNode } from "react";
import { errorMessage } from "../api/client";
import { humanize } from "../lib/format";
import { Icon, type IconName } from "./Icon";

export function cx(...values: Array<string | false | null | undefined>): string {
  return values.filter(Boolean).join(" ");
}

interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: "primary" | "secondary" | "danger" | "ghost";
  icon?: IconName;
  busy?: boolean;
}

export function Button({ variant = "secondary", icon, busy, children, className, disabled, ...props }: ButtonProps) {
  return (
    <button className={cx("button", `button--${variant}`, className)} disabled={disabled || busy} {...props}>
      {busy ? <span className="spinner spinner--small" aria-hidden="true" /> : icon ? <Icon name={icon} /> : null}
      <span>{children}</span>
    </button>
  );
}

export function StatusChip({ status, compact = false }: { status: string; compact?: boolean }) {
  const tone = ["online", "ready", "idle", "succeeded", "approved", "completed", "open"].includes(status)
    ? "positive"
    : ["running", "starting", "dispatching", "provisioning", "connecting"].includes(status)
      ? "active"
      : ["waiting_approval", "pending", "enrolling", "draining", "retrying"].includes(status)
        ? "warning"
        : ["error", "failed", "offline", "revoked", "denied", "critical", "closed"].includes(status)
          ? "negative"
          : "neutral";
  return (
    <span className={cx("status-chip", `status-chip--${tone}`, compact && "status-chip--compact")}>
      <span className="status-chip__dot" aria-hidden="true" />
      {humanize(status)}
    </span>
  );
}

export function PageHeader({
  eyebrow,
  title,
  description,
  actions
}: {
  eyebrow?: string;
  title: string;
  description?: string;
  actions?: ReactNode;
}) {
  return (
    <header className="page-header">
      <div>
        {eyebrow ? <p className="eyebrow">{eyebrow}</p> : null}
        <h1>{title}</h1>
        {description ? <p className="page-header__description">{description}</p> : null}
      </div>
      {actions ? <div className="page-header__actions">{actions}</div> : null}
    </header>
  );
}

export function LoadingState({ label = "Loading" }: { label?: string }) {
  return (
    <div className="state-panel state-panel--loading" role="status">
      <span className="spinner" aria-hidden="true" />
      <p>{label}</p>
    </div>
  );
}

export function ErrorState({ error, retry }: { error: unknown; retry?: () => void }) {
  return (
    <div className="state-panel state-panel--error" role="alert">
      <span className="state-panel__icon"><Icon name="deny" size={22} /></span>
      <div>
        <h2>Couldn’t load this view</h2>
        <p>{errorMessage(error)}</p>
      </div>
      {retry ? <Button icon="refresh" onClick={retry}>Try again</Button> : null}
    </div>
  );
}

export function EmptyState({
  icon = "box",
  title,
  description,
  action
}: {
  icon?: IconName;
  title: string;
  description: string;
  action?: ReactNode;
}) {
  return (
    <div className="empty-state">
      <span className="empty-state__icon"><Icon name={icon} size={25} /></span>
      <h2>{title}</h2>
      <p>{description}</p>
      {action ? <div className="empty-state__action">{action}</div> : null}
    </div>
  );
}

export function Modal({
  open,
  title,
  description,
  onClose,
  children,
  size = "medium"
}: {
  open: boolean;
  title: string;
  description?: string;
  onClose: () => void;
  children: ReactNode;
  size?: "small" | "medium" | "large";
}) {
  const ref = useRef<HTMLDialogElement>(null);

  useEffect(() => {
    const dialog = ref.current;
    if (!dialog) return;
    if (open && !dialog.open) dialog.showModal();
    if (!open && dialog.open) dialog.close();
  }, [open]);

  const closeFromBackdrop = (event: FormEvent<HTMLDialogElement>) => {
    if (event.target === ref.current) onClose();
  };

  return (
    <dialog ref={ref} className={cx("modal", `modal--${size}`)} onCancel={onClose} onClick={closeFromBackdrop}>
      <div className="modal__surface">
        <header className="modal__header">
          <div>
            <h2>{title}</h2>
            {description ? <p>{description}</p> : null}
          </div>
          <button className="icon-button" type="button" onClick={onClose} aria-label={`Close ${title}`}>
            <Icon name="close" />
          </button>
        </header>
        {children}
      </div>
    </dialog>
  );
}

export function RefreshButton({ refreshing, onClick }: { refreshing?: boolean; onClick: () => void }) {
  return <Button variant="ghost" icon="refresh" busy={refreshing} onClick={onClick}>Refresh</Button>;
}

export function InlineAlert({ children, tone = "error" }: { children: ReactNode; tone?: "error" | "success" | "warning" }) {
  return <div className={cx("inline-alert", `inline-alert--${tone}`)} role={tone === "error" ? "alert" : "status"}>{children}</div>;
}
