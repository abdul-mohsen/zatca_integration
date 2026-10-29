package processor

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// ProtocolInitialPIH is the explicit protocol bootstrap value used when a
// branch is initialized after onboarding. Normal document processing reads the
// value from durable branch state and never falls back to this constant.
const ProtocolInitialPIH = "NWZlY2ViNjZmZmM4NmYzOGQ5NTI3ODZjNmQ2OTZjNzljMmRiYzIzOWRkNGU5MWI0NjcyOWQ3M2EyN2ZiNTdlOQ=="

// DocumentType identifies the document table that owns a chain position.
type DocumentType string

const (
	DocumentBill   DocumentType = "bill"
	DocumentCredit DocumentType = "credit"
	DocumentDebit  DocumentType = "debit"
)

const (
	chainPositionReserved = "reserved"
	chainPositionAccepted = "accepted"
)

var (
	ErrChainStateUninitialized = errors.New("ZATCA chain state is uninitialized")
	ErrChainPositionConflict   = errors.New("ZATCA chain position conflict")
	ErrChainPositionOutOfOrder = errors.New("ZATCA chain position is out of order")
)

// ChainState is the latest accepted state for one EGS/branch.
type ChainState struct {
	BranchID            int64
	PreviousInvoiceHash string
	LastICV             uint64
}

// ChainDocument identifies one durable document reservation.
type ChainDocument struct {
	BranchID        int64
	Type            DocumentType
	DocumentID      int64
	ProposedUUID    string
	ProposedIssueAt time.Time
}

// ChainPosition is the PIH/ICV position and stable retry identity allocated to
// one document.
type ChainPosition struct {
	Document            ChainDocument
	ICV                 uint64
	PreviousInvoiceHash string
	InvoiceUUID         string
	IssueAt             time.Time
	Status              string
	SignedXML           string
	InvoiceHash         string
}

// ChainAcceptance contains the values committed after an accepted provider
// response.
type ChainAcceptance struct {
	InvoiceHash string
	InvoiceUUID string
	QR          string
}

// ChainStateStore owns reservation and accepted-state transitions for one
// tenant's EGS/branch chains.
type ChainStateStore interface {
	Reserve(document ChainDocument) (ChainPosition, error)
	SavePrepared(position ChainPosition, signedXML, invoiceHash string) error
	CommitAccepted(position ChainPosition, acceptance ChainAcceptance) error
}

func validateChainDocument(document ChainDocument) error {
	if document.BranchID <= 0 {
		return fmt.Errorf("branch ID must be positive")
	}
	if !document.Type.valid() {
		return fmt.Errorf("unsupported chain document type %q", document.Type)
	}
	if document.DocumentID <= 0 {
		return fmt.Errorf("document ID must be positive")
	}
	return nil
}

func (t DocumentType) valid() bool {
	return t == DocumentBill || t == DocumentCredit || t == DocumentDebit
}

func positionKey(document ChainDocument) string {
	return fmt.Sprintf("%d:%s:%d", document.BranchID, document.Type, document.DocumentID)
}

// MemoryChainStateStore is a deterministic reference implementation used by
// processor tests and by callers that need an explicit non-SQL contract.
type MemoryChainStateStore struct {
	mu        sync.Mutex
	states    map[int64]ChainState
	positions map[string]ChainPosition
}

func NewMemoryChainStateStore(states map[int64]ChainState) *MemoryChainStateStore {
	copyStates := make(map[int64]ChainState, len(states))
	for branchID, state := range states {
		if state.BranchID == 0 {
			state.BranchID = branchID
		}
		copyStates[branchID] = state
	}
	return &MemoryChainStateStore{
		states:    copyStates,
		positions: make(map[string]ChainPosition),
	}
}

func (s *MemoryChainStateStore) Reserve(document ChainDocument) (ChainPosition, error) {
	if err := validateChainDocument(document); err != nil {
		return ChainPosition{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	state, ok := s.states[document.BranchID]
	if !ok {
		return ChainPosition{}, fmt.Errorf("branch %d: %w", document.BranchID, ErrChainStateUninitialized)
	}
	if strings.TrimSpace(state.PreviousInvoiceHash) == "" {
		return ChainPosition{}, fmt.Errorf("branch %d: %w (previous invoice hash is empty)", document.BranchID, ErrChainStateUninitialized)
	}

	key := positionKey(document)
	if position, ok := s.positions[key]; ok {
		return position, nil
	}

	highest := state.LastICV
	for _, position := range s.positions {
		if position.Document.BranchID == document.BranchID && position.ICV > highest {
			highest = position.ICV
		}
	}
	if highest == math.MaxUint64 {
		return ChainPosition{}, fmt.Errorf("branch %d: ICV overflow", document.BranchID)
	}

	position := ChainPosition{
		Document:            document,
		ICV:                 highest + 1,
		PreviousInvoiceHash: strings.TrimSpace(state.PreviousInvoiceHash),
		InvoiceUUID:         document.ProposedUUID,
		IssueAt:             document.ProposedIssueAt,
		Status:              chainPositionReserved,
	}
	if position.InvoiceUUID == "" {
		position.InvoiceUUID = uuid.New().String()
	}
	if position.IssueAt.IsZero() {
		position.IssueAt = time.Now().UTC()
	}
	s.positions[key] = position
	return position, nil
}

func (s *MemoryChainStateStore) SavePrepared(position ChainPosition, signedXML, invoiceHash string) error {
	if strings.TrimSpace(signedXML) == "" || strings.TrimSpace(invoiceHash) == "" {
		return fmt.Errorf("prepared signed XML and invoice hash are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	key := positionKey(position.Document)
	stored, ok := s.positions[key]
	if !ok {
		return fmt.Errorf("chain position %s not found", key)
	}
	if stored.Status == chainPositionAccepted {
		if stored.InvoiceHash != invoiceHash {
			return fmt.Errorf("%w: accepted hash changed for %s", ErrChainPositionConflict, key)
		}
		return nil
	}
	stored.SignedXML = signedXML
	stored.InvoiceHash = invoiceHash
	s.positions[key] = stored
	return nil
}

func (s *MemoryChainStateStore) CommitAccepted(position ChainPosition, acceptance ChainAcceptance) error {
	if strings.TrimSpace(acceptance.InvoiceHash) == "" {
		return fmt.Errorf("accepted invoice hash is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	key := positionKey(position.Document)
	stored, ok := s.positions[key]
	if !ok {
		return fmt.Errorf("chain position %s not found", key)
	}
	if stored.Status == chainPositionAccepted {
		if stored.InvoiceHash != acceptance.InvoiceHash {
			return fmt.Errorf("%w: accepted hash changed for %s", ErrChainPositionConflict, key)
		}
		if acceptance.InvoiceUUID != "" && stored.InvoiceUUID != acceptance.InvoiceUUID {
			return fmt.Errorf("%w: accepted UUID changed for %s", ErrChainPositionConflict, key)
		}
		return nil
	}
	if acceptance.InvoiceUUID != "" && stored.InvoiceUUID != acceptance.InvoiceUUID {
		return fmt.Errorf("%w: accepted UUID changed for %s", ErrChainPositionConflict, key)
	}

	state, ok := s.states[position.Document.BranchID]
	if !ok {
		return fmt.Errorf("branch %d: %w", position.Document.BranchID, ErrChainStateUninitialized)
	}
	if state.LastICV == math.MaxUint64 ||
		stored.ICV != state.LastICV+1 ||
		stored.PreviousInvoiceHash != strings.TrimSpace(state.PreviousInvoiceHash) {
		return fmt.Errorf("%w: branch=%d icv=%d last_icv=%d", ErrChainPositionOutOfOrder, position.Document.BranchID, stored.ICV, state.LastICV)
	}

	state.LastICV = stored.ICV
	state.PreviousInvoiceHash = acceptance.InvoiceHash
	s.states[position.Document.BranchID] = state
	stored.Status = chainPositionAccepted
	stored.InvoiceHash = acceptance.InvoiceHash
	if acceptance.InvoiceUUID != "" {
		stored.InvoiceUUID = acceptance.InvoiceUUID
	}
	s.positions[key] = stored
	return nil
}

func (s *MemoryChainStateStore) State(branchID int64) (ChainState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.states[branchID]
	return state, ok
}

// SQLChainStateStore persists reservations and accepted state in the tenant
// database. Its schema is part of the repository-local T017 contract.
type SQLChainStateStore struct {
	db *sql.DB
}

func NewSQLChainStateStore(db *sql.DB) *SQLChainStateStore {
	return &SQLChainStateStore{db: db}
}

func (s *SQLChainStateStore) Reserve(document ChainDocument) (ChainPosition, error) {
	if err := validateChainDocument(document); err != nil {
		return ChainPosition{}, err
	}
	if s == nil || s.db == nil {
		return ChainPosition{}, fmt.Errorf("T017 chain state contract unavailable: database is not configured")
	}

	tx, err := s.db.Begin()
	if err != nil {
		return ChainPosition{}, fmt.Errorf("begin chain reservation: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	position, err := scanSQLChainPosition(tx.QueryRow(`
		SELECT icv, previous_invoice_hash, invoice_uuid, issue_at,
		       signed_xml, invoice_hash, status
		  FROM zatca_chain_position
		 WHERE branch_id = ? AND doc_type = ? AND document_id = ?
		 FOR UPDATE`,
		document.BranchID, document.Type, document.DocumentID,
	), document)
	if err == nil {
		if err := tx.Commit(); err != nil {
			return ChainPosition{}, fmt.Errorf("commit existing chain reservation: %w", err)
		}
		committed = true
		return position, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ChainPosition{}, chainContractError(err)
	}

	previousHash, lastICV, err := scanSQLBranchState(tx, document.BranchID)
	if err != nil {
		return ChainPosition{}, chainContractError(err)
	}
	if previousHash == "" {
		return ChainPosition{}, fmt.Errorf("branch %d: %w; provision previous_invoice_hash through the T016/backend contract", document.BranchID, ErrChainStateUninitialized)
	}
	// A concurrent reservation for the same document may have committed while
	// this transaction waited for the branch row lock. Re-read the document
	// key after acquiring that lock before allocating a new ICV.
	position, err = scanSQLChainPosition(tx.QueryRow(`
		SELECT icv, previous_invoice_hash, invoice_uuid, issue_at,
		       signed_xml, invoice_hash, status
		  FROM zatca_chain_position
		 WHERE branch_id = ? AND doc_type = ? AND document_id = ?
		 FOR UPDATE`,
		document.BranchID, document.Type, document.DocumentID,
	), document)
	if err == nil {
		if err := tx.Commit(); err != nil {
			return ChainPosition{}, fmt.Errorf("commit concurrent chain reservation: %w", err)
		}
		committed = true
		return position, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ChainPosition{}, chainContractError(err)
	}

	var maxRaw string
	if err := tx.QueryRow(`
		SELECT COALESCE(CAST(MAX(icv) AS CHAR), '0')
		  FROM zatca_chain_position
		 WHERE branch_id = ?`, document.BranchID).Scan(&maxRaw); err != nil {
		return ChainPosition{}, chainContractError(err)
	}
	maxICV, err := parseICV(maxRaw)
	if err != nil {
		return ChainPosition{}, chainContractError(err)
	}
	if maxICV < lastICV {
		maxICV = lastICV
	}
	if maxICV == math.MaxUint64 {
		return ChainPosition{}, fmt.Errorf("branch %d: ICV overflow", document.BranchID)
	}

	position = ChainPosition{
		Document:            document,
		ICV:                 maxICV + 1,
		PreviousInvoiceHash: previousHash,
		InvoiceUUID:         document.ProposedUUID,
		IssueAt:             document.ProposedIssueAt,
		Status:              chainPositionReserved,
	}
	if position.InvoiceUUID == "" {
		position.InvoiceUUID = uuid.New().String()
	}
	if position.IssueAt.IsZero() {
		position.IssueAt = time.Now().UTC()
	}

	_, err = tx.Exec(`
		INSERT INTO zatca_chain_position
			(branch_id, doc_type, document_id, icv, previous_invoice_hash,
			 invoice_uuid, issue_at, signed_xml, invoice_hash, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, '', NULL, ?)`,
		document.BranchID, document.Type, document.DocumentID, position.ICV,
		position.PreviousInvoiceHash, position.InvoiceUUID,
		position.IssueAt.UnixNano(), chainPositionReserved,
	)
	if err != nil {
		return ChainPosition{}, chainContractError(err)
	}
	if err := tx.Commit(); err != nil {
		return ChainPosition{}, fmt.Errorf("commit new chain reservation: %w", err)
	}
	committed = true
	return position, nil
}

func (s *SQLChainStateStore) SavePrepared(position ChainPosition, signedXML, invoiceHash string) error {
	if strings.TrimSpace(signedXML) == "" || strings.TrimSpace(invoiceHash) == "" {
		return fmt.Errorf("prepared signed XML and invoice hash are required")
	}
	if s == nil || s.db == nil {
		return fmt.Errorf("T017 chain state contract unavailable: database is not configured")
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin prepared chain update: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	stored, err := scanSQLChainPosition(tx.QueryRow(`
		SELECT icv, previous_invoice_hash, invoice_uuid, issue_at,
		       signed_xml, invoice_hash, status
		  FROM zatca_chain_position
		 WHERE branch_id = ? AND doc_type = ? AND document_id = ?
		 FOR UPDATE`,
		position.Document.BranchID, position.Document.Type, position.Document.DocumentID,
	), position.Document)
	if err != nil {
		return chainContractError(err)
	}
	if stored.Status == chainPositionAccepted {
		if stored.InvoiceHash != invoiceHash {
			return fmt.Errorf("%w: accepted hash changed for %s", ErrChainPositionConflict, positionKey(position.Document))
		}
		return nil
	}
	if _, err := tx.Exec(`
		UPDATE zatca_chain_position
		   SET signed_xml = ?, invoice_hash = ?
		 WHERE branch_id = ? AND doc_type = ? AND document_id = ?`,
		signedXML, invoiceHash,
		position.Document.BranchID, position.Document.Type, position.Document.DocumentID,
	); err != nil {
		return chainContractError(err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit prepared chain update: %w", err)
	}
	committed = true
	return nil
}

func (s *SQLChainStateStore) CommitAccepted(position ChainPosition, acceptance ChainAcceptance) error {
	if strings.TrimSpace(acceptance.InvoiceHash) == "" {
		return fmt.Errorf("accepted invoice hash is required")
	}
	if s == nil || s.db == nil {
		return fmt.Errorf("T017 chain state contract unavailable: database is not configured")
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin accepted chain update: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	stored, err := scanSQLChainPosition(tx.QueryRow(`
		SELECT icv, previous_invoice_hash, invoice_uuid, issue_at,
		       signed_xml, invoice_hash, status
		  FROM zatca_chain_position
		 WHERE branch_id = ? AND doc_type = ? AND document_id = ?
		 FOR UPDATE`,
		position.Document.BranchID, position.Document.Type, position.Document.DocumentID,
	), position.Document)
	if err != nil {
		return chainContractError(err)
	}
	if stored.Status == chainPositionAccepted {
		if stored.InvoiceHash != acceptance.InvoiceHash {
			return fmt.Errorf("%w: accepted hash changed for %s", ErrChainPositionConflict, positionKey(position.Document))
		}
		if acceptance.InvoiceUUID != "" && stored.InvoiceUUID != acceptance.InvoiceUUID {
			return fmt.Errorf("%w: accepted UUID changed for %s", ErrChainPositionConflict, positionKey(position.Document))
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit duplicate accepted chain update: %w", err)
		}
		committed = true
		return nil
	}

	previousHash, lastICV, err := scanSQLBranchState(tx, position.Document.BranchID)
	if err != nil {
		return chainContractError(err)
	}
	if lastICV == math.MaxUint64 ||
		stored.ICV != lastICV+1 ||
		stored.PreviousInvoiceHash != previousHash {
		return fmt.Errorf("%w: branch=%d icv=%d last_icv=%d", ErrChainPositionOutOfOrder, position.Document.BranchID, stored.ICV, lastICV)
	}

	invoiceUUID := stored.InvoiceUUID
	if acceptance.InvoiceUUID != "" {
		if invoiceUUID != "" && invoiceUUID != acceptance.InvoiceUUID {
			return fmt.Errorf("%w: accepted UUID changed for %s", ErrChainPositionConflict, positionKey(position.Document))
		}
		invoiceUUID = acceptance.InvoiceUUID
	}
	table, qrColumn, ok := documentTable(position.Document.Type)
	if !ok {
		return fmt.Errorf("unsupported chain document type %q", position.Document.Type)
	}
	result, err := tx.Exec(fmt.Sprintf(
		`UPDATE %s SET invoice_hash = ?, %s = ?, invoice_uuid = ?, state = 3 WHERE id = ?`,
		table, qrColumn,
	), acceptance.InvoiceHash, acceptance.QR, invoiceUUID, position.Document.DocumentID)
	if err != nil {
		return chainContractError(err)
	}
	if rows, err := result.RowsAffected(); err != nil || rows != 1 {
		if err != nil {
			return chainContractError(err)
		}
		return fmt.Errorf("accepted document %s %d was not updated", position.Document.Type, position.Document.DocumentID)
	}

	result, err = tx.Exec(`
		UPDATE branch_zatca_config
		   SET previous_invoice_hash = ?, last_icv = ?
		 WHERE branch_id = ?`,
		acceptance.InvoiceHash, stored.ICV, position.Document.BranchID,
	)
	if err != nil {
		return chainContractError(err)
	}
	if rows, err := result.RowsAffected(); err != nil || rows != 1 {
		if err != nil {
			return chainContractError(err)
		}
		return fmt.Errorf("branch %d accepted chain state was not updated", position.Document.BranchID)
	}

	if _, err := tx.Exec(`
		UPDATE zatca_chain_position
		   SET invoice_uuid = ?, invoice_hash = ?, status = ?, accepted_at = NOW()
		 WHERE branch_id = ? AND doc_type = ? AND document_id = ?`,
		invoiceUUID, acceptance.InvoiceHash, chainPositionAccepted,
		position.Document.BranchID, position.Document.Type, position.Document.DocumentID,
	); err != nil {
		return chainContractError(err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit accepted chain update: %w", err)
	}
	committed = true
	return nil
}

func documentTable(documentType DocumentType) (table, qrColumn string, ok bool) {
	switch documentType {
	case DocumentBill:
		return "bill", "qr_code", true
	case DocumentCredit:
		return "credit_note", "invoice_qr", true
	case DocumentDebit:
		return "debit_note", "invoice_qr", true
	default:
		return "", "", false
	}
}

type chainRowScanner interface {
	Scan(dest ...any) error
}

func scanSQLChainPosition(scanner chainRowScanner, document ChainDocument) (ChainPosition, error) {
	var (
		icvRaw       string
		previousHash string
		invoiceUUID  string
		issueRaw     string
		signedXML    sql.NullString
		invoiceHash  sql.NullString
		status       string
	)
	if err := scanner.Scan(
		&icvRaw, &previousHash, &invoiceUUID, &issueRaw,
		&signedXML, &invoiceHash, &status,
	); err != nil {
		return ChainPosition{}, err
	}
	icv, err := parseICV(icvRaw)
	if err != nil {
		return ChainPosition{}, err
	}
	issueAt, err := parseIssueAt(issueRaw)
	if err != nil {
		return ChainPosition{}, err
	}
	return ChainPosition{
		Document:            document,
		ICV:                 icv,
		PreviousInvoiceHash: strings.TrimSpace(previousHash),
		InvoiceUUID:         invoiceUUID,
		IssueAt:             issueAt,
		Status:              status,
		SignedXML:           signedXML.String,
		InvoiceHash:         invoiceHash.String,
	}, nil
}

func scanSQLBranchState(tx *sql.Tx, branchID int64) (string, uint64, error) {
	var previousHash, lastRaw string
	if err := tx.QueryRow(`
		SELECT COALESCE(previous_invoice_hash, ''), CAST(last_icv AS CHAR)
		  FROM branch_zatca_config
		 WHERE branch_id = ?
		 FOR UPDATE`, branchID).Scan(&previousHash, &lastRaw); err != nil {
		return "", 0, err
	}
	lastICV, err := parseICV(lastRaw)
	if err != nil {
		return "", 0, err
	}
	return strings.TrimSpace(previousHash), lastICV, nil
}

func parseICV(raw string) (uint64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid ICV %q: %w", raw, err)
	}
	return value, nil
}

func parseIssueAt(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, nil
	}
	nanos, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid issue timestamp %q: %w", raw, err)
	}
	return time.Unix(0, nanos).UTC(), nil
}

func chainContractError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("T017 chain state contract unavailable or invalid: %w", err)
}

// InitializeBranchChainState explicitly bootstraps a newly onboarded branch.
// It never overwrites an existing chain state and therefore is not a runtime
// fallback for missing state.
func InitializeBranchChainState(db *sql.DB, branchID int64) error {
	if db == nil {
		return fmt.Errorf("initialize branch %d chain state: database is not configured", branchID)
	}
	_, err := db.Exec(`
		UPDATE branch_zatca_config
		   SET previous_invoice_hash = ?, last_icv = 0
		 WHERE branch_id = ?
		   AND COALESCE(previous_invoice_hash, '') = ''
		   AND last_icv = 0`,
		ProtocolInitialPIH, branchID,
	)
	if err != nil {
		return fmt.Errorf("initialize branch %d chain state: %w", branchID, err)
	}
	return nil
}
