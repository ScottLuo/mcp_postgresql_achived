// Package security: approval.go provides server-side approval-record binding.
//
// Certain operations (e.g. dropping an index) may only run after an explicit
// approval has been granted. An approval is bound to a specific target, tool,
// and set of normalized parameters; changing any of those invalidates the
// approval. Expiry is also enforced so a stale approval cannot be reused.
package security

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// ApprovalRecord is a granted approval, bound to a specific operation.
type ApprovalRecord struct {
	ID               string    // unique approval ID
	Target           string    // target alias
	Tool             string    // tool name
	NormalizedParams string    // JSON-normalized operation parameters or SQL
	Expiry           time.Time // approval expires at this time
	Approver         string    // identity of the approver
	CreatedAt        time.Time // when approval was granted
}

// ApprovalStore persists approval records. Implementations must be safe for
// concurrent use.
type ApprovalStore interface {
	Create(record *ApprovalRecord) error
	Get(id string) (*ApprovalRecord, error)
	Invalidate(id string) error
}

// InMemoryApprovalStore is a mutex-protected in-memory ApprovalStore. In a
// production deployment this would be backed by a persistent store or an
// external approval system; the in-memory implementation is deterministic and
// dependency-free.
type InMemoryApprovalStore struct {
	mu      sync.RWMutex
	records map[string]*ApprovalRecord
}

// NewInMemoryApprovalStore returns an empty in-memory approval store.
func NewInMemoryApprovalStore() *InMemoryApprovalStore {
	return &InMemoryApprovalStore{records: make(map[string]*ApprovalRecord)}
}

// Create stores the record, overwriting any existing record with the same ID.
func (s *InMemoryApprovalStore) Create(record *ApprovalRecord) error {
	if record == nil || record.ID == "" {
		return fmt.Errorf("%s: approval record must have a non-empty ID", ErrCodeApprovalRequired)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Store a copy so subsequent mutations of the caller's record do not affect
	// the stored one.
	cp := *record
	s.records[record.ID] = &cp
	return nil
}

// Get returns a copy of the record with the given ID, or an error if it does not
// exist.
func (s *InMemoryApprovalStore) Get(id string) (*ApprovalRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	record, ok := s.records[id]
	if !ok {
		return nil, fmt.Errorf("%s: no approval record found for ID %s", ErrCodeApprovalRequired, id)
	}
	cp := *record
	return &cp, nil
}

// Invalidate removes the record with the given ID.
func (s *InMemoryApprovalStore) Invalidate(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.records[id]; !ok {
		return fmt.Errorf("%s: no approval record found for ID %s", ErrCodeApprovalRequired, id)
	}
	delete(s.records, id)
	return nil
}

// NormalizeParams deterministically serializes operation parameters to a JSON
// string. Go's encoding/json marshals map keys in sorted order, so the output is
// stable for a given set of keys/values, allowing reliable equality comparison
// against a previously stored approval.
func NormalizeParams(params map[string]any) (string, error) {
	if params == nil {
		return "{}", nil
	}
	b, err := json.Marshal(params)
	if err != nil {
		return "", fmt.Errorf("%s: normalize params: %v", ErrCodeApprovalInvalid, err)
	}
	return string(b), nil
}

// ApprovalValidator validates approval records against an ApprovalStore.
type ApprovalValidator struct {
	store ApprovalStore
}

// NewApprovalValidator returns an ApprovalValidator backed by the given store.
func NewApprovalValidator(store ApprovalStore) *ApprovalValidator {
	return &ApprovalValidator{store: store}
}

// Validate checks that the approval identified by approvalID is valid for the
// given target, tool, and normalized params. It returns an error carrying
// ErrCodeApprovalRequired if no record exists, or ErrCodeApprovalInvalid if the
// record does not match or has expired.
func (v *ApprovalValidator) Validate(ctx context.Context, approvalID, target, tool, params string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	record, err := v.store.Get(approvalID)
	if err != nil {
		// Get already returns ErrCodeApprovalRequired when the record is missing.
		return err
	}

	if record.Target != target || record.Tool != tool {
		return fmt.Errorf("%s: approval target/tool mismatch", ErrCodeApprovalInvalid)
	}

	if record.NormalizedParams != params {
		return fmt.Errorf("%s: approval parameters changed", ErrCodeApprovalInvalid)
	}

	if !time.Now().Before(record.Expiry) {
		return fmt.Errorf("%s: approval expired at %s", ErrCodeApprovalInvalid, record.Expiry.Format(time.RFC3339))
	}

	return nil
}
