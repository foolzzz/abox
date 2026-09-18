import { useState } from "react";
import { api, errorMessage } from "../api/client";
import type { Notification } from "../api/types";
import { Icon } from "../components/Icon";
import { useToast } from "../components/Toast";
import { Button, EmptyState, ErrorState, InlineAlert, LoadingState, PageHeader, RefreshButton, StatusChip, cx } from "../components/ui";
import { useResource } from "../hooks/useResource";
import { formatDate, humanize, relativeTime } from "../lib/format";
import { Link } from "../lib/router";

export function NotificationsView() {
  const { notify } = useToast();
  const resource = useResource((signal) => api.listNotifications(signal), []);
  const [updating, setUpdating] = useState<string>();
  const [error, setError] = useState<string>();
  const [filter, setFilter] = useState<"all" | "unread">("all");

  if (resource.loading) return <LoadingState label="Loading notifications" />;
  if (resource.error && !resource.data) return <ErrorState error={resource.error} retry={resource.reload} />;

  const notifications = [...(resource.data ?? [])].sort((left, right) => new Date(right.createdAt).getTime() - new Date(left.createdAt).getTime());
  const unread = notifications.filter((item) => item.status === "unread");
  const visible = filter === "unread" ? unread : notifications;

  const markRead = async (item: Notification) => {
    if (item.status === "read" || updating) return;
    setUpdating(item.id);
    setError(undefined);
    try {
      const updated = await api.markNotificationRead(item.id);
      resource.setData((current) => current?.map((candidate) => candidate.id === item.id ? { ...candidate, ...updated, status: "read" } : candidate));
      window.dispatchEvent(new Event("agentbox:notifications-changed"));
    } catch (requestError) {
      setError(errorMessage(requestError));
    } finally {
      setUpdating(undefined);
    }
  };

  const markAllRead = async () => {
    setUpdating("all");
    setError(undefined);
    try {
      await api.markAllNotificationsRead();
      const readAt = new Date().toISOString();
      resource.setData((current) => current?.map((item) => ({ ...item, status: "read", readAt: item.readAt ?? readAt })));
      window.dispatchEvent(new Event("agentbox:notifications-changed"));
      notify("All notifications marked as read.");
    } catch (requestError) {
      setError(errorMessage(requestError));
    } finally {
      setUpdating(undefined);
    }
  };

  return (
    <div className="page">
      <PageHeader eyebrow="Personal inbox" title="Notifications" description="Approval deadlines, unattended runs, automation failures, and box activity that need your attention." actions={<><RefreshButton refreshing={resource.refreshing} onClick={resource.reload} />{unread.length ? <Button icon="check" busy={updating === "all"} onClick={() => void markAllRead()}>Mark all read</Button> : null}</>} />
      {resource.error ? <InlineAlert tone="warning">Notifications could not be refreshed. Showing the last loaded inbox.</InlineAlert> : null}
      {error ? <InlineAlert>{error}</InlineAlert> : null}
      {notifications.length ? (
        <>
          <div className="notification-tabs" role="tablist" aria-label="Notification filter"><button className={cx(filter === "all" && "notification-tabs__active")} role="tab" aria-selected={filter === "all"} onClick={() => setFilter("all")}>All <span>{notifications.length}</span></button><button className={cx(filter === "unread" && "notification-tabs__active")} role="tab" aria-selected={filter === "unread"} onClick={() => setFilter("unread")}>Unread <span>{unread.length}</span></button></div>
          {visible.length ? <section className="notification-list" aria-label={`${humanize(filter)} notifications`}>{visible.map((item) => <NotificationRow item={item} busy={updating === item.id} onRead={() => void markRead(item)} key={item.id} />)}</section> : <EmptyState icon="notification" title="You’re all caught up" description="There are no unread notifications." />}
        </>
      ) : <EmptyState icon="notification" title="No notifications" description="Approval requests, schedule outcomes, and operational alerts will appear here." />}
    </div>
  );
}

function NotificationRow({ item, busy, onRead }: { item: Notification; busy: boolean; onRead: () => void }) {
  const href = item.boxId ? `/boxes/${item.boxId}/agent-terminal` : item.scheduleId ? "/schedules" : undefined;
  const details = <><span><strong>{item.title}</strong>{item.status === "unread" ? <i aria-label="Unread" /> : null}</span><p>{item.body}</p><small><span>{humanize(item.type)}</span><time dateTime={item.createdAt} title={formatDate(item.createdAt)}>{relativeTime(item.createdAt)}</time>{item.readAt ? <span>Read {relativeTime(item.readAt)}</span> : null}</small></>;
  return (
    <article className={cx("notification-row", item.status === "unread" && "notification-row--unread")}>
      <span className={cx("notification-row__icon", `notification-row__icon--${notificationTone(item.type)}`)}><Icon name={notificationIcon(item.type)} /></span>
      {href ? <Link className="notification-row__content" to={href} onClick={onRead}>{details}</Link> : <div className="notification-row__content">{details}</div>}
      <span className="notification-row__actions">{busy ? <span className="spinner spinner--small" /> : item.status === "unread" ? <button type="button" onClick={onRead}>Mark read</button> : <StatusChip status="read" compact />}{href ? <Icon name="chevron" /> : null}</span>
    </article>
  );
}

function notificationIcon(type: string): "approval" | "schedule" | "box" | "notification" {
  const normalized = type.toLowerCase();
  if (normalized.includes("approval")) return "approval";
  if (normalized.includes("schedule") || normalized.includes("execution") || normalized.includes("unattended")) return "schedule";
  if (normalized.includes("box") || normalized.includes("run")) return "box";
  return "notification";
}

function notificationTone(type: string): "warning" | "negative" | "positive" | "neutral" {
  const normalized = type.toLowerCase();
  if (normalized.includes("fail") || normalized.includes("error") || normalized.includes("expired")) return "negative";
  if (normalized.includes("approval") || normalized.includes("pending")) return "warning";
  if (normalized.includes("success") || normalized.includes("complete")) return "positive";
  return "neutral";
}
