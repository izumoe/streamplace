package director

import (
	"bytes"
	"context"
	"flag"
	"log/slog"
	"testing"

	slogGorm "github.com/orandin/slog-gorm"
	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/config"
	"stream.place/streamplace/pkg/model"
	"stream.place/streamplace/pkg/placestream"
)

func TestLivestreamRecordingEligibility(t *testing.T) {
	previousVerbosity := flag.Lookup("v").Value.String()
	require.NoError(t, flag.Set("v", "3"))
	t.Cleanup(func() { require.NoError(t, flag.Set("v", previousVerbosity)) })
	var logs bytes.Buffer
	logger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(logger) })
	gormLogger := config.GormLogger
	config.GormLogger = slogGorm.New(slogGorm.WithHandler(slog.Default().Handler()))
	t.Cleanup(func() { config.GormLogger = gormLogger })
	const creator = "did:plc:streamer"
	on, off := true, false
	for _, tc := range []struct {
		name      string
		allowed   []string
		settings  bool
		recording *bool
		badRecord bool
		closedDB  bool
		want      bool
		wantLog   string
	}{
		{name: "open node defaults to recording", want: true},
		{name: "allowed account defaults to recording", allowed: []string{creator}, want: true},
		{name: "account outside allowlist is not recorded", allowed: []string{"did:plc:other"}},
		{name: "explicit consent permits recording", allowed: []string{creator}, settings: true, recording: &on, want: true},
		{name: "explicit opt out prevents recording", allowed: []string{creator}, settings: true, recording: &off},
		{name: "unrelated settings retain recording default", settings: true, want: true},
		{name: "consent cannot override operator restriction", allowed: []string{"did:plc:other"}, settings: true, recording: &on},
		{name: "malformed settings fail closed", settings: true, badRecord: true, wantLog: "failed to decode server settings"},
		{name: "settings lookup failure fails closed", closedDB: true, wantLog: "failed to load server settings"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, err := model.MakeDB(":memory:")
			require.NoError(t, err)
			db, err := m.(*model.DBModel).DB.DB()
			require.NoError(t, err)
			if tc.settings {
				var record bytes.Buffer
				require.NoError(t, (&placestream.ServerSettings{LivestreamRecording: tc.recording}).MarshalCBOR(&record))
				data := record.Bytes()
				if tc.badRecord {
					data = []byte("invalid CBOR")
				}
				require.NoError(t, m.UpdateServerSettings(context.Background(), &model.ServerSettings{Server: "stream.example", RepoDID: creator, Record: &data}))
			}
			if tc.closedDB {
				require.NoError(t, db.Close())
			} else {
				t.Cleanup(func() { require.NoError(t, db.Close()) })
			}
			logs.Reset()
			ss := &StreamSession{mod: m, cli: &config.CLI{BroadcasterHost: "stream.example", AllowedStreams: tc.allowed}}
			require.Equal(t, tc.want, ss.shouldRecordLivestream(context.Background(), creator))
			if tc.wantLog != "" {
				require.Contains(t, logs.String(), tc.wantLog)
			} else {
				require.NotContains(t, logs.String(), "level=ERROR")
			}
		})
	}
}
