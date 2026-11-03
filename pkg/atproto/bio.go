package atproto

import (
	"context"
	"fmt"

	"github.com/bluesky-social/indigo/xrpc"
	"stream.place/streamplace/pkg/constants"
	"stream.place/streamplace/pkg/reposync"
)

// SyncBioPage fills bio coverage for repositories indexed before bios were supported.
func (atsync *ATProtoSynchronizer) SyncBioPage(ctx context.Context, did string) error {
	indexed, err := atsync.Model.HasIndexedBioPage(ctx, did)
	if err != nil || indexed {
		return err
	}
	repo, err := atsync.SyncBlueskyRepoCached(ctx, did)
	if err != nil {
		return err
	}
	if repo.TerminalStatus() {
		return nil
	}
	ident, err := atsync.resolveIdent(ctx, did, true)
	if err != nil {
		return err
	}
	lock := handleLocks.GetLock(did)
	lock.Lock()
	defer lock.Unlock()
	ctx = markWalking(ctx, did)
	defer markSyncInFlight(did)()
	indexed, err = atsync.Model.HasIndexedBioPage(ctx, did)
	if err != nil || indexed {
		return err
	}
	// The exact singleton range excludes neighboring rkeys such as selfExtra.
	path := constants.PLACE_STREAM_BIO_PAGE + "/self"
	client := &xrpc.Client{Host: ident.PDSEndpoint(), Client: SyncHTTPClient}
	_, _, err = atsync.walkBackfill(ctx, ident, client, []reposync.KeyRange{{Lo: []byte(path), Hi: []byte(path + "\x00")}})
	if err != nil && !isStaleWalkError(err) && isMethodNotSupported(err) {
		// Older hosts require the same full-CAR fallback as first sync.
		_, err = atsync.legacyBackfill(ctx, ident, client)
	}
	if err != nil {
		return fmt.Errorf("failed to index bio coverage: %w", err)
	}
	return nil
}
