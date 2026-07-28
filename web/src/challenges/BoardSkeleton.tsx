import { Skeleton } from "../ui";
import "./challenges.css";

// A skeleton in the shape of the board, not a spinner on white: the page that arrives should be
// the page that was promised. It lives apart from the board itself so it can be shown *while* the
// board's chunk is still downloading, without dragging the board into the eager bundle.
export function BoardSkeleton() {
  return (
    <>
      {[0, 1].map((section) => (
        <section className="board__category" key={section}>
          <div className="board__category-head">
            <Skeleton width="8rem" height="1.25rem" />
          </div>
          <div className="board__grid">
            {[0, 1, 2, 3].map((card) => (
              <div className="ff-card chal-card" key={card}>
                <Skeleton height="1.25rem" />
                <Skeleton width="60%" height="0.875rem" />
              </div>
            ))}
          </div>
        </section>
      ))}
    </>
  );
}
