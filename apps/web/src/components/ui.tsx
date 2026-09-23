import { useEffect, useRef, type ButtonHTMLAttributes, type FormEvent, type ReactNode } from "react";
import { errorMessage } from "../api/client";
import { humanize } from "../lib/format";
import { useI18n } from "../lib/i18n";
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
const STATUS_ZH: Record<string, string> = {
  online: "在线", ready: "就绪", idle: "空闲", running: "运行中", starting: "启动中",
  dispatching: "正在下发", waiting_approval: "等待审批", pending: "待处理", approved: "已批准",
  denied: "已拒绝", completed: "已完成", succeeded: "成功", failed: "失败", error: "错误",
  offline: "离线", available: "可用", unavailable: "不可用", paused: "已暂停", active: "已启用",
  hibernating: "休眠中", hibernated: "已休眠", cancelled: "已取消", expired: "已过期",
  read_only: "只读", operator: "操作者", admin: "管理员", user: "普通用户", owner: "所有者", viewer: "查看者", disabled: "已禁用",
  validation: "等待验证", validate_on_create: "创建时验证", connecting: "连接中", connected: "已连接", closed: "已关闭"
};


export function StatusChip({ status, compact = false }: { status: string; compact?: boolean }) {
  const { locale } = useI18n();
  const tone = ["online", "ready", "idle", "succeeded", "approved", "completed", "open", "available", "owner", "active"].includes(status)
    ? "positive"
    : ["running", "starting", "dispatching", "claimed", "provisioning", "connecting", "operator", "admin", "in_progress"].includes(status)
      ? "active"
      : ["waiting_approval", "pending", "enrolling", "draining", "retrying", "read_only", "paused", "hibernating", "hibernated", "skipped", "parked"].includes(status)
        ? "warning"
        : ["error", "failed", "offline", "revoked", "denied", "critical", "closed", "unavailable", "cancelled", "expired", "deleted", "disabled", "blocked", "abandoned"].includes(status)
          ? "negative"
          : "neutral";
  return (
    <span className={cx("status-chip", `status-chip--${tone}`, compact && "status-chip--compact")}>
      <span className="status-chip__dot" aria-hidden="true" />
      {locale === "zh-CN" ? STATUS_ZH[status] ?? humanize(status) : humanize(status)}
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
  const { t } = useI18n();
  return <Button variant="ghost" icon="refresh" busy={refreshing} onClick={onClick}>{t("common.refresh")}</Button>;
}

export function InlineAlert({ children, tone = "error" }: { children: ReactNode; tone?: "error" | "success" | "warning" }) {
  return <div className={cx("inline-alert", `inline-alert--${tone}`)} role={tone === "error" ? "alert" : "status"}>{children}</div>;
}
