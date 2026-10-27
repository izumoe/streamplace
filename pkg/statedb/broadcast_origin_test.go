package statedb

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/config"
)

func TestBroadcastOriginPreservesReportedTime(t *testing.T) {
	db, err := MakeDB(t.Context(), &config.CLI{DBURL: ":memory:"}, nil, nil)
	require.NoError(t, err)
	for _, age := range []time.Duration{2 * time.Minute, 3 * time.Minute} {
		reported := time.Now().Add(-age).UTC()
		require.NoError(t, db.UpsertBroadcastOrigin("did:plc:streamer", "did:web:origin.example", "moqt://origin.example:38443", reported))
		origin, err := db.GetLatestBroadcastOriginForStreamer("did:plc:streamer")
		require.NoError(t, err)
		require.NotNil(t, origin)
		require.WithinDuration(t, reported, origin.UpdatedAt, time.Microsecond, "inserts and updates preserve the origin's reported liveness")
	}
}
