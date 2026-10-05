package upstreamstate

import "context"

// CatalogResponse is transient probe data, never included in status snapshots.
// Consumers must validate the complete payload before storing it.
type CatalogResponse struct {
	Route  string
	Source string
	Body   []byte
}

func (m *Monitor) deliverCatalogs(ctx context.Context, revision uint64, responses []CatalogResponse) {
	if m.onCatalog == nil || len(responses) == 0 || ctx.Err() != nil || m.channel.Snapshot().Revision != revision {
		return
	}
	m.onCatalog(ctx, responses)
}
