package vod

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/hyphacoop/go-dasl/drisl"

	"stream.place/streamplace/pkg/bdasl"
	"stream.place/streamplace/pkg/blob"
	"stream.place/streamplace/pkg/muxl"
)

// The flat-MP4 VOD model: the stored blob is [flat-header][canonical fragments],
// content-addressed by the MUXL CID — the BDASL hash of the fragments ALONE,
// not the assembled blob. Keying off the immutable fragments means a better
// flat-header can be re-synthesized and rewritten in place without changing a
// VOD's id (or any media.origin/media.track record), and one blob serves both
// flat-MP4 (the whole file) and HLS (per-track init blobs + byte-ranges into
// the fragments at flatHeaderSize+offset).

// openFragmentReaders opens the canonical fragment objects as sequential
// readers in order. The caller closes the returned closers.
func openFragmentReaders(ctx context.Context, store blob.Store, keys []string) ([]io.Reader, []io.Closer, error) {
	readers := make([]io.Reader, 0, len(keys))
	closers := make([]io.Closer, 0, len(keys))
	for _, key := range keys {
		r, err := store.Open(ctx, key)
		if err != nil {
			for _, c := range closers {
				_ = c.Close()
			}
			return nil, nil, fmt.Errorf("open object %s: %w", key, err)
		}
		closers = append(closers, r)
		readers = append(readers, io.NewSectionReader(r, 0, r.Size()))
	}
	return readers, closers, nil
}

func closeAll(closers []io.Closer) {
	for _, c := range closers {
		_ = c.Close()
	}
}

// hashAndBuildFragmentMetafile streams the bare canonical fragments once,
// teeing into a bdasl hasher (→ the MUXL CID) and muxl unwrap → a
// fragment-relative metafileBuilder (→ the HLS metafile + per-track init
// blobs). The MUXL CID hashes the fragments alone, so it's stable across
// header re-synthesis. Returns the MUXL CID, the fragments' total size, and the
// metafile (with fragment-relative segment offsets). physicalMetafiles is the
// same native metadata stream used to synthesize the flat header.
func hashAndBuildFragmentMetafile(ctx context.Context, store blob.Store, keys []string, physicalMetafiles []byte) (string, int64, *Metafile, error) {
	readers, closers, err := openFragmentReaders(ctx, store, keys)
	if err != nil {
		return "", 0, nil, err
	}
	defer closeAll(closers)

	hasher := bdasl.NewWriter()
	counter := &countingWriter{}
	tee := io.TeeReader(io.MultiReader(readers...), io.MultiWriter(hasher, counter))

	mb := newFragmentMetafileBuilder(ctx, store)
	eventCh := make(chan *muxl.MuxlEvent, 16)
	producerErr := make(chan error, 1)
	go func() {
		producerErr <- muxl.RunMuxlUnwrapEvents(ctx, tee, eventCh)
		close(eventCh)
	}()

	var obsErr error
	for ev := range eventCh {
		if e := mb.Observe(ev); e != nil && obsErr == nil {
			obsErr = e
		}
	}
	if perr := <-producerErr; perr != nil {
		return "", 0, nil, fmt.Errorf("muxl unwrap: %w", perr)
	}
	if obsErr != nil {
		return "", 0, nil, fmt.Errorf("metafile build: %w", obsErr)
	}
	if err := ctx.Err(); err != nil {
		return "", 0, nil, err
	}

	muxlCID := hasher.CID()
	size := counter.load()
	meta := mb.Finalize(muxlCID, size)
	// Unwrap groups tracks by presentation time. Its event order need not
	// match the recorded bytes; native metafiles describe physical order.
	decoder := drisl.NewDecoder(bytes.NewReader(physicalMetafiles))
	positions := make(map[string]int, len(meta.Tracks))
	var offset int64
	for {
		var event struct {
			TrackByteSizes map[string]uint64 `cbor:"track_byte_sizes"`
		}
		if err := decoder.Decode(&event); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return "", 0, nil, fmt.Errorf("decode physical metafile: %w", err)
		}
		if len(event.TrackByteSizes) != 1 {
			return "", 0, nil, fmt.Errorf("physical metafile must describe one track, got %d", len(event.TrackByteSizes))
		}
		for id, n := range event.TrackByteSizes {
			track, ok := meta.Tracks[id]
			i := positions[id]
			if !ok || i >= len(track.Segments) || n == 0 || n > uint64(size-offset) {
				return "", 0, nil, fmt.Errorf("physical metafile for track %s does not match archive at offset %d", id, offset)
			}
			track.Segments[i].Offset = offset
			track.Segments[i].Size = int64(n)
			positions[id]++
			offset += int64(n)
		}
	}
	if offset != size {
		return "", 0, nil, fmt.Errorf("physical metafiles describe %d bytes, archive contains %d", offset, size)
	}
	for id, track := range meta.Tracks {
		if positions[id] != len(track.Segments) {
			return "", 0, nil, fmt.Errorf("physical metafiles describe %d fragments for track %s, archive contains %d", positions[id], id, len(track.Segments))
		}
	}
	return muxlCID, size, meta, nil
}

// synthFlatHeaderForObjects emits the metafile stream for the canonical
// fragments and synthesizes the flat-MP4 faststart header from it.
func synthFlatHeaderForObjects(ctx context.Context, store blob.Store, keys []string) ([]byte, []byte, error) {
	readers, closers, err := openFragmentReaders(ctx, store, keys)
	if err != nil {
		return nil, nil, err
	}
	defer closeAll(closers)
	return synthFlatHeader(ctx, io.MultiReader(readers...))
}

// synthFlatHeader synthesizes the flat-MP4 faststart header for a stream of
// bare canonical fragments. muxl owns all the co64 offset math over the
// [header][fragments] layout, so the returned header is exactly the bytes to
// prepend to the fragments. The returned native metafile stream also carries
// the physical segment order used by the recording's HLS index.
func synthFlatHeader(ctx context.Context, fragments io.Reader) ([]byte, []byte, error) {
	var metas bytes.Buffer
	if err := muxl.RunMuxlMetafiles(ctx, fragments, &metas); err != nil {
		return nil, nil, fmt.Errorf("emit metafiles: %w", err)
	}
	var header bytes.Buffer
	if err := muxl.RunMuxlSynthesizeFlatHeader(ctx, bytes.NewReader(metas.Bytes()), &header); err != nil {
		return nil, nil, fmt.Errorf("synthesize flat header: %w", err)
	}
	if header.Len() == 0 {
		return nil, nil, errors.New("muxl produced an empty flat header")
	}
	return header.Bytes(), metas.Bytes(), nil
}
