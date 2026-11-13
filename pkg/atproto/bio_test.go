package atproto

import (
	"bytes"
	"context"
	"testing"

	indigoatproto "github.com/bluesky-social/indigo/api/atproto"
	"github.com/bluesky-social/indigo/atproto/syntax"
	atrepo "github.com/bluesky-social/indigo/repo"
	"github.com/ipfs/go-cid"
	car "github.com/ipld/go-car"
	carutil "github.com/ipld/go-car/util"
	"github.com/multiformats/go-multihash"
	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/model"
	"stream.place/streamplace/pkg/placestream"
	"stream.place/streamplace/pkg/spid"
)

func TestBioIndexSelf(t *testing.T) {
	ctx := context.Background()
	atsync, mod, _ := offlineSynchronizer(t)
	const did = "did:plc:aaaaaaaaaaaaaaaaaaaaaaaa"
	require.NoError(t, mod.UpdateRepo(&model.Repo{
		DID: did, Handle: "bio.test", PDS: "http://127.0.0.1:1", Version: "3lrev00000000",
	}))
	for _, tc := range []struct {
		path string
		text string
		want string
		rev  string
	}{
		{"place.stream.bio.page/self", "original", "original", "3lq4mksr7i22a"},
		{"place.stream.bio.page/other", "wrong key", "original", "3lq4mksr7i22b"},
		{"place.stream.bio.page/self", "edited", "edited", "3lq4mksr7i22c"},
		{"place.stream.bio.page/self", "stale edit", "edited", "3lq4mksr7i22b"},
		{"place.stream.chat.profile/self", "wrong collection", "edited", "3lq4mksr7i22d"},
	} {
		rec := &placestream.BioPage{
			CreatedAt:   "2026-10-02T00:00:00Z",
			Description: &placestream.BioPage_Description{Plaintext: tc.text},
		}
		var buf bytes.Buffer
		require.NoError(t, rec.MarshalCBOR(&buf))
		record := buf.Bytes()
		cid, err := spid.GetCID(rec)
		require.NoError(t, err)
		collection, rkey, err := syntax.ParseRepoPath(tc.path)
		require.NoError(t, err)
		require.NoError(t, atsync.handleCreateUpdate(ctx, did, rkey, &record, cid.String(), collection, false, false, tc.rev))
		row, err := mod.GetBioPage(ctx, did)
		require.NoError(t, err)
		require.NotNil(t, row)
		page, err := row.ToStreamplaceBioPage()
		require.NoError(t, err)
		require.Equal(t, tc.want, page.Description.Plaintext)
	}
}

func TestBioDeleteSelf(t *testing.T) {
	ctx := context.Background()
	atsync, mod, _ := offlineSynchronizer(t)
	const did = "did:plc:aaaaaaaaaaaaaaaaaaaaaaaa"
	require.NoError(t, mod.UpdateRepo(&model.Repo{DID: did, Handle: "bio.test", Version: "3lrev00000000"}))
	var record bytes.Buffer
	require.NoError(t, (&placestream.BioPage{CreatedAt: "2026-10-02T00:00:00Z"}).MarshalCBOR(&record))
	encoded := record.Bytes()
	require.NoError(t, mod.CreateBioPage(ctx, &model.BioPage{RepoDID: did, RepoRev: "3lq4mksr7i22a", Record: &encoded}))

	// Delete commits carry the commit block without the deleted record.
	prefix := cid.Prefix{Version: 1, Codec: cid.DagCBOR, MhType: multihash.SHA2_256, MhLength: -1}
	data, err := prefix.Sum([]byte{0xa0})
	require.NoError(t, err)
	commit := atrepo.SignedCommit{Did: did, Version: 3, Data: data, Rev: "3lq4mksr7i22c"}
	var commitBlock bytes.Buffer
	require.NoError(t, commit.MarshalCBOR(&commitBlock))
	root, err := prefix.Sum(commitBlock.Bytes())
	require.NoError(t, err)
	var blocks bytes.Buffer
	require.NoError(t, car.WriteHeader(&car.CarHeader{Version: 1, Roots: []cid.Cid{root}}, &blocks))
	require.NoError(t, carutil.LdWrite(&blocks, root.Bytes(), commitBlock.Bytes()))

	for _, rkey := range []string{"other", "self"} {
		require.True(t, atsync.handleIndexedOps(ctx, &indigoatproto.SyncSubscribeRepos_Commit{
			Repo: did, Rev: "3lq4mksr7i22c", Time: "2026-10-02T00:00:00Z", Blocks: blocks.Bytes(),
			Ops: []*indigoatproto.SyncSubscribeRepos_RepoOp{repoOp("delete", "place.stream.bio.page/"+rkey)},
		}))
		page, err := mod.GetBioPage(ctx, did)
		require.NoError(t, err)
		if rkey == "other" {
			require.NotNil(t, page, "another record key must not delete the bio")
		} else {
			require.Nil(t, page, "deleting self must remove the public bio")
		}
	}
}
