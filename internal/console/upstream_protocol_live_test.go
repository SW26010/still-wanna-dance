package console

import (
	"context"
	"os"
	"testing"
	"time"

	"still-wanna-dance/internal/applog"
	"still-wanna-dance/internal/playbacksource"
	"still-wanna-dance/internal/upstreamcheck"
)

// Uses production DoH, candidate selection, validation and storage in an
// isolated library. Reads one video byte; never starts the gateway or VRC.
func TestLiveFixedUpstreamProtocols(t *testing.T) {
	if os.Getenv("SWD_LIVE_FIXED_PROTOCOL") != "1" {
		t.Skip("opt-in live HTTP policy check")
	}
	c := testConsole(t)
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	access := c.connectivityAccess()
	catalog, _, response := access.Catalog(ctx, "api")
	if catalog.State != "available" || catalog.Scheme != "http" {
		t.Fatalf("catalog: state=%s scheme=%s error=%v", catalog.State, catalog.Scheme, applog.SafeError(catalog.Err))
	}
	c.syncCatalogs(ctx, []upstreamcheck.CatalogResponse{response})
	status, err := c.store.CatalogStatus(ctx)
	if err != nil || status.Revision != catalog.CatalogTime {
		t.Fatal("catalog not stored", status, err)
	}
	t.Logf("Udon HTTP catalog: status=%d bytes=%d revision=%s", catalog.HTTP, len(response.Body), catalog.CatalogTime)
	const song = 6254
	playback, sample, address := access.Playback(ctx, song, playbacksource.Entries()[0])
	if playback.State != "available" || playback.Scheme != "http" || sample == nil || address.API != playbacksource.DefaultAPI {
		t.Fatalf("playback: state=%s scheme=%s error=%v", playback.State, playback.Scheme, applog.SafeError(playback.Err))
	}
	c.syncPlaybackURL(ctx, address)
	urls, err := c.store.SongURLs(ctx, song, "cf")
	if err != nil || len(urls) != 1 || urls[0].URL != address.URL {
		t.Fatal("playback evidence not stored", err)
	}
	t.Logf("playback HTTP: status=%d song=%d resource=%s", playback.HTTP, song, sample.Host)
	resource := access.Resource(ctx, song, *sample)
	if resource.State != "available" || resource.Scheme != "http" || resource.HTTP != 206 {
		t.Fatalf("resource: state=%s scheme=%s status=%d error=%v", resource.State, resource.Scheme, resource.HTTP, applog.SafeError(resource.Err))
	}
	t.Logf("resource HTTP: status=%d range=bytes=0-0", resource.HTTP)
}
