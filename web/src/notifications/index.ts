// Notifications: one live feed, three surfaces — the bell, the drawer, the page.
//
// The shell mounts <NotificationsDrawer/> once (it owns the session's single EventSource
// subscription and the toasts, and it must stay mounted with the panel shut) and puts a
// <NotificationBell/> in the topbar. Both need a <ToastProvider> above them.

export { NotificationsDrawer } from "./NotificationsDrawer";
export { NotificationBell, type NotificationBellProps } from "./NotificationBell";
export {
  NotificationList,
  NotificationsEmpty,
  NotificationsError,
  NotificationsSkeleton,
  type NotificationListProps,
  type NotificationsErrorProps,
} from "./NotificationList";
export { useNotifications, type Feed } from "./useNotifications";
export {
  closeNotifications,
  markReadThrough,
  openNotifications,
  toggleNotifications,
  useNotificationsUI,
  type NotificationsUI,
} from "./store";
export {
  excerpt,
  isFirstBlood,
  mergeNotifications,
  newestId,
  type Notification,
} from "./feed";
