import { useEffect, useRef } from "react";
import { useQuery } from "@tanstack/react-query";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { notificationsQuery } from "../../queries";
import { Pagination } from "../../ui";
import {
  NotificationList,
  NotificationsEmpty,
  NotificationsError,
  NotificationsSkeleton,
  markReadThrough,
  newestId,
  useNotificationsUI,
} from "../../notifications";

const PER_PAGE = 25;

export const Route = createFileRoute("/_auth/notifications")({
  validateSearch: (search: Record<string, unknown>): { page: number } => {
    const page = Number(search.page);
    return { page: Number.isSafeInteger(page) && page > 0 ? page : 1 };
  },
  component: NotificationsPage,
});

/**
 * The archive. The drawer shows the live feed; this pages through everything behind it.
 *
 * Deliberately not merged with the stream: a page 3 that grows a row at the top whenever an
 * announcement lands would shuffle the rows under the reader. The drawer is where live belongs.
 */
function NotificationsPage() {
  const { page } = Route.useSearch();
  const navigate = useNavigate({ from: Route.fullPath });
  const { readThrough } = useNotificationsUI();

  const query = useQuery(notificationsQuery({ page, per_page: PER_PAGE }));
  const items = query.data?.notifications ?? [];
  const newest = newestId(items);

  // Reading the archive is reading them: the bell must not keep claiming they are new.
  useEffect(() => {
    if (page === 1 && newest > 0) markReadThrough(newest);
  }, [page, newest]);

  return (
    <div className="ff-notif-page">
      <div className="page-head">
        <h1>notifications</h1>
        {query.data !== undefined && (
          <span className="ff-notif-page__status">{query.data.total} in total</span>
        )}
      </div>

      {query.isPending ? (
        <NotificationsSkeleton rows={6} />
      ) : query.isError ? (
        <NotificationsError error={query.error} onRetry={() => void query.refetch()} />
      ) : items.length === 0 ? (
        <NotificationsEmpty />
      ) : (
        <>
          <NotificationList items={items} readThrough={readThrough} />
          <Pagination
            page={page}
            perPage={PER_PAGE}
            total={query.data.total}
            onPageChange={(next) => void navigate({ search: { page: next } })}
          />
        </>
      )}
    </div>
  );
}
