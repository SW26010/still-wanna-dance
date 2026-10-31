package upstreamstate

import (
	"context"

	"still-wanna-dance/internal/upstreamcheck"
)

// CatalogResponse is transient probe data, never included in status snapshots.
// Consumers must validate the complete payload before storing it.
type CatalogResponse = upstreamcheck.CatalogResponse

func (m *Monitor) deliverCatalogs(ctx context.Context, revision uint64, responses []CatalogResponse) {
	if m.onCatalog == nil || len(responses) == 0 || ctx.Err() != nil || m.channel.Snapshot().Revision != revision {
		return
	}
	m.onCatalog(ctx, responses)
}
