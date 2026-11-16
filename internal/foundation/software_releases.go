package foundation

import (
	"context"

	"github.com/thtpark/fdn/internal/domain"
)

func (s *Store) SoftwareReleases(ctx context.Context, actor, product, cursor string, limit int) (domain.Page[domain.SoftwareRelease], error) {
	page := domain.Page[domain.SoftwareRelease]{Items: []domain.SoftwareRelease{}}
	if _, err := s.actor(ctx, s.db, actor); err != nil {
		return page, err
	}
	if _, err := get[domain.SoftwareProduct](ctx, s.db, "software_products", product); err != nil {
		return page, err
	}
	limit, err := limitSize(limit)
	if err != nil {
		return page, err
	}
	key := "software-releases:" + product
	last, err := readCursor(cursor, actor, key)
	if err != nil {
		return page, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT data FROM software_releases WHERE product_id=? AND id>? ORDER BY id LIMIT ?", product, last, limit+1)
	if err != nil {
		return page, err
	}
	page.Items, err = scanJSON[domain.SoftwareRelease](rows)
	if err != nil {
		return page, err
	}
	if len(page.Items) > limit {
		page.NextCursor = makeCursor(actor, key, page.Items[limit-1].ID)
		page.Items = page.Items[:limit]
	}
	return page, nil
}
