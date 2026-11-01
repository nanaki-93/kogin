// A reviewer's findings or notes: a few words each, the line they are about
// as a link to it, and the detail on demand.

import type { LoadedSession, ReviewItem } from '@/api/types';
import { reviewItemPlace, reviewItemRoute } from '@/lib/workflow';
import { navigate } from '@/lib/router';
import { cx } from '@/components/ui';

export function ReviewItems({ items, session, advisory, links = true }: { items: ReviewItem[]; session: LoadedSession; advisory?: boolean; links?: boolean }) {
  return (
    <ul className={cx('findings', advisory && 'findings-advisory')}>
      {items.map((item, i) => (
        <ReviewItemRow key={i} item={item} session={session} links={links} />
      ))}
    </ul>
  );
}

function ReviewItemRow({ item, session, links }: { item: ReviewItem; session: LoadedSession; links: boolean }) {
  const place = reviewItemPlace(item);
  const route = links ? reviewItemRoute(session, item) : null;
  const where = `${item.path}${item.line ? ` at line ${item.line}` : ''}`;
  return (
    <li className="review-item">
      <div className="review-item-head">
        <span className="review-item-title">{item.title}</span>
        {route ? (
          <button type="button" className="link-btn mono xs review-place" title={`Open ${where}`} onClick={() => navigate(route.path, route.params)}>
            {place}
          </button>
        ) : place ? (
          <span className="mono xs faint" title={item.path}>{place}</span>
        ) : null}
      </div>
      {item.detail ? (
        <details className="review-item-detail">
          <summary className="xs muted">Why</summary>
          <p className="small muted">{item.detail}</p>
        </details>
      ) : null}
    </li>
  );
}
