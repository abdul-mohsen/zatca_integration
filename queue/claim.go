package queue

import (
	"context"
	"errors"
)

// ClaimDecision is the outcome of claiming one delivery.
type ClaimDecision uint8

const (
	// ClaimProcess means the request is new, or its lease expired, so the delivery must be processed.
	ClaimProcess ClaimDecision = iota
	// ClaimDuplicate means the request is already complete, so the delivery is only acknowledged.
	ClaimDuplicate
	// ClaimConflict means the request identity was reused with different content.
	ClaimConflict
	// ClaimInFlight means another delivery holds a live lease on the request.
	ClaimInFlight
)

var (
	// ErrFingerprintConflict means a request identity was reused with different content.
	ErrFingerprintConflict = errors.New("queue request fingerprint conflict")
	// ErrClaimInFlight means another delivery currently holds the claim.
	ErrClaimInFlight = errors.New("queue claim is in flight")
	// ErrClaimNotFound means a completion was attempted without a claim.
	ErrClaimNotFound = errors.New("queue claim not found")
	// ErrClaimStale means a lease owner tried to release or complete after
	// another delivery took over the request.
	ErrClaimStale = errors.New("queue claim lease is stale")
)

// ClaimStore records which requests were processed, so a redelivery never repeats the business effect. A claim whose
// delivery failed technically is released, so the redelivery can take it at once.
type ClaimStore interface {
	Claim(context.Context, Message) (ClaimDecision, error)
	Complete(context.Context, Message) error
	Release(context.Context, Message) error
}
