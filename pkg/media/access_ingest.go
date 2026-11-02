package media

import (
	"context"
	"fmt"

	"stream.place/streamplace/pkg/access"
)

type publisherAdmission struct {
	did        string
	generation uint64
}

type publisherAdmissionKey struct{}

func (mm *MediaManager) publisherDecision(did string) access.Decision {
	if mm.Access != nil {
		return mm.Access.Check(did, access.Streamer)
	}
	err := mm.cli.StreamIsAllowed(did)
	if err != nil {
		return access.Decision{Reason: err.Error()}
	}
	return access.Decision{Allowed: true}
}

// CheckPublisherGeneration also rejects work from a revoked session after the
// account is approved again. A new connection captures the new generation.
func (mm *MediaManager) CheckPublisherGeneration(did string, generation uint64) error {
	decision := mm.publisherDecision(did)
	if !decision.Allowed {
		return fmt.Errorf("publisher %s is not allowed: %s", did, decision.Reason)
	}
	if decision.Generation != generation {
		return fmt.Errorf("publisher %s must reconnect after access changed", did)
	}
	return nil
}

// PublisherContext carries an admitted session into asynchronous publication.
func (mm *MediaManager) PublisherContext(ctx context.Context, did string, generation uint64) (context.Context, error) {
	if err := mm.CheckPublisherGeneration(did, generation); err != nil {
		return nil, err
	}
	admission := publisherAdmission{did: did, generation: generation}
	if current, ok := ctx.Value(publisherAdmissionKey{}).(publisherAdmission); ok && current == admission {
		return ctx, nil
	}
	return context.WithValue(ctx, publisherAdmissionKey{}, admission), nil
}

// CheckPublisherContext leaves remote carriage alone; only local admissions
// carry a publisher generation.
func (mm *MediaManager) CheckPublisherContext(ctx context.Context) error {
	if admitted, ok := ctx.Value(publisherAdmissionKey{}).(publisherAdmission); ok {
		return mm.CheckPublisherGeneration(admitted.did, admitted.generation)
	}
	return nil
}

func (mm *MediaManager) admitPublisher(ctx context.Context, did string) (context.Context, error) {
	if admitted, ok := ctx.Value(publisherAdmissionKey{}).(publisherAdmission); ok {
		if admitted.did != did {
			return nil, fmt.Errorf("publisher %s does not match admitted identity %s", did, admitted.did)
		}
		if err := mm.CheckPublisherGeneration(did, admitted.generation); err != nil {
			return nil, err
		}
	} else {
		decision := mm.publisherDecision(did)
		if !decision.Allowed {
			return nil, fmt.Errorf("publisher %s is not allowed: %s", did, decision.Reason)
		}
		ctx = context.WithValue(ctx, publisherAdmissionKey{}, publisherAdmission{did: did, generation: decision.Generation})
	}
	if ingestSessionFromContext(ctx) == 0 {
		ctx = withIngestSession(ctx, mm.nextIngestSession())
	}
	return ctx, nil
}

func (mm *MediaManager) checkPublisherContext(ctx context.Context, did string) (uint64, error) {
	ctx, err := mm.admitPublisher(ctx, did)
	if err != nil {
		return 0, err
	}
	return ctx.Value(publisherAdmissionKey{}).(publisherAdmission).generation, nil
}

func (mm *MediaManager) resumePublisher(ctx context.Context, meta workerMeta) (context.Context, error) {
	if mm.Access != nil && meta.PublisherGeneration == nil {
		return nil, fmt.Errorf("worker has no publisher admission metadata")
	}
	if meta.PublisherGeneration != nil {
		ctx = context.WithValue(ctx, publisherAdmissionKey{}, publisherAdmission{did: meta.StreamerDID, generation: *meta.PublisherGeneration})
	}
	return mm.admitPublisher(ctx, meta.StreamerDID)
}
