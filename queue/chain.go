package queue

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// GenesisPIH is the previous-invoice hash of a branch's first invoice: base64 of the SHA-256 of "0".
const GenesisPIH = "NWZlY2ViNjZmZmM4NmYzOGQ5NTI3ODZjNmQ2OTZjNzljMmRiYzIzOWRkNGU5MWI0NjcyOWQ3M2EyN2ZiNTdlOQ=="

// Outcome states of one invoice. An accepted, warned, or rejected invoice is final.
const (
	OutcomeAccepted = "accepted"
	OutcomeWarning  = "accepted_with_warning"
	OutcomeRejected = "rejected"
)

// Invoice kinds select the ZATCA endpoint: a standard invoice is cleared, a simplified one is reported.
const (
	InvoiceStandard   = "standard"
	InvoiceSimplified = "simplified"
)

var (
	// ErrChainBusy means another request holds the branch's chain, and its lease has not expired.
	ErrChainBusy = errors.New("branch invoice chain is busy")
	// ErrChainConflict means the branch's chain no longer matches the booking, so nothing was committed.
	ErrChainConflict = errors.New("branch invoice chain changed")
	// ErrRejectedInput means a document cannot be invoiced as it is. Retrying would fail the same way.
	ErrRejectedInput = errors.New("document rejected before invoicing")
)

const defaultBookingLease = 5 * time.Minute

// Booking is the number a bill is built with: its ICV, the hash it chains from, and the UUID from its envelope. A booking
// holds the branch's chain until it is committed or released.
type Booking struct {
	BranchID   int64
	RequestKey string
	EventID    string
	UUID       string
	ICV        uint64
	PIH        string
}

// SignedInvoice is one built and signed bill. Commit stores it once, and its XML, hash, and ICV never change afterwards.
type SignedInvoice struct {
	SourceID  int64
	BranchID  int64
	UUID      string
	ICV       uint64
	PIH       string
	Hash      string
	Kind      string
	SignedXML string
	QR        string
}

// Outcome is ZATCA's final answer for one invoice.
type Outcome struct {
	State        string
	ProviderCode string
	Message      string
}

// Chain is the invoice chain of each branch. The daemon books the next number, commits the signed invoice and advances the
// chain in one transaction, and records what ZATCA answered.
type Chain interface {
	Stored(ctx context.Context, msg Message) (*SignedInvoice, error)
	Book(ctx context.Context, msg Message) (Booking, error)
	Release(ctx context.Context, booking Booking) error
	Commit(ctx context.Context, booking Booking, invoice SignedInvoice) error
	Record(ctx context.Context, invoice SignedInvoice, outcome Outcome) error
}

// SQLChain is the chain in the tenant database. A booking uses the reservation columns of branch_zatca_config (goose 00005).
// The committed chain is last_icv and previous_invoice_hash on the same row. The invoice itself is a zatca_submission row.
type SQLChain struct {
	DB            *sql.DB
	LeaseDuration time.Duration
	Now           func() time.Time
}

// NewSQLChain returns the chain of one tenant database.
func NewSQLChain(db *sql.DB) *SQLChain {
	return &SQLChain{DB: db, LeaseDuration: defaultBookingLease}
}

func (c *SQLChain) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *SQLChain) leaseDuration() time.Duration {
	if c.LeaseDuration <= 0 {
		return defaultBookingLease
	}
	return c.LeaseDuration
}

// chainRow is the chain state of one branch, read under a row lock.
type chainRow struct {
	lastICV     uint64
	previous    sql.NullString
	ownerKey    sql.NullString
	ownerToken  sql.NullString
	reservedICV sql.NullInt64
	leaseEnd    nullableTime
}

const chainRowSQL = `
	SELECT last_icv, previous_invoice_hash, icv_reservation_key, icv_reservation_token,
	       reserved_icv, reservation_lease_expires_at
	FROM branch_zatca_config
	WHERE branch_id = ?`

func loadChainRow(ctx context.Context, tx *sql.Tx, branchID int64, noWait bool) (chainRow, error) {
	query := chainRowSQL + " FOR UPDATE"
	if noWait {
		query += " NOWAIT"
	}
	var row chainRow
	err := tx.QueryRowContext(ctx, query, branchID).Scan(
		&row.lastICV, &row.previous, &row.ownerKey, &row.ownerToken, &row.reservedICV, &row.leaseEnd,
	)
	return row, err
}

// effectivePIH is the hash the next invoice chains from. A branch that has issued nothing chains from the genesis hash.
func (r chainRow) effectivePIH() string {
	if r.previous.Valid && r.previous.String != "" {
		return r.previous.String
	}
	return GenesisPIH
}

func (r chainRow) ownedBy(key string) bool {
	return r.ownerKey.Valid && r.ownerKey.String == key
}

func (r chainRow) leaseLive(now time.Time) bool {
	return r.leaseEnd.Valid && r.leaseEnd.Time.After(now)
}

// holds reports whether the row carries exactly this booking, so a commit only applies to the number that was booked.
func (r chainRow) holds(b Booking) bool {
	return r.ownedBy(b.RequestKey) &&
		r.ownerToken.Valid && r.ownerToken.String == b.EventID &&
		r.reservedICV.Valid && uint64(r.reservedICV.Int64) == b.ICV &&
		r.lastICV+1 == b.ICV && r.effectivePIH() == b.PIH
}

// Book reserves the next ICV for a request. Another request's booking blocks it while the lease is live. A request
// resumes its own booking after a crash, and the lease of a crashed booking expires.
func (c *SQLChain) Book(ctx context.Context, msg Message) (Booking, error) {
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return Booking{}, fmt.Errorf("begin booking: %w", err)
	}
	defer tx.Rollback()

	row, err := loadChainRow(ctx, tx, msg.BranchID, true)
	switch {
	case isMySQLError(err, mysqlLockNoWait):
		return Booking{}, ErrChainBusy
	case errors.Is(err, sql.ErrNoRows):
		return Booking{}, fmt.Errorf("branch %d has no invoice chain: %w", msg.BranchID, ErrRejectedInput)
	case err != nil:
		return Booking{}, fmt.Errorf("lock branch %d chain: %w", msg.BranchID, err)
	}

	booking := Booking{
		BranchID:   msg.BranchID,
		RequestKey: msg.RequestKey(),
		EventID:    msg.EventID,
		UUID:       msg.InvoiceUUID,
		ICV:        row.lastICV + 1,
		PIH:        row.effectivePIH(),
	}
	now := c.now()
	if row.ownerKey.Valid && !row.ownedBy(booking.RequestKey) && row.leaseLive(now) {
		return Booking{}, ErrChainBusy
	}
	if row.ownedBy(booking.RequestKey) && row.reservedICV.Valid && uint64(row.reservedICV.Int64) != booking.ICV {
		return Booking{}, ErrChainConflict
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE branch_zatca_config
		SET icv_reservation_key = ?, icv_reservation_token = ?, reserved_icv = ?, reservation_lease_expires_at = ?
		WHERE branch_id = ?
	`, booking.RequestKey, booking.EventID, booking.ICV, now.Add(c.leaseDuration()), booking.BranchID); err != nil {
		return Booking{}, fmt.Errorf("book ICV %d of branch %d: %w", booking.ICV, booking.BranchID, err)
	}
	if err := tx.Commit(); err != nil {
		return Booking{}, fmt.Errorf("commit booking of branch %d: %w", booking.BranchID, err)
	}
	return booking, nil
}

// Release drops a booking after a failure before the commit. It only touches the booking's own reservation, so a late
// release after another booking took over does nothing.
func (c *SQLChain) Release(ctx context.Context, booking Booking) error {
	if _, err := c.DB.ExecContext(ctx, `
		UPDATE branch_zatca_config
		SET icv_reservation_key = NULL, icv_reservation_token = NULL, reserved_icv = NULL, reservation_lease_expires_at = NULL
		WHERE branch_id = ? AND icv_reservation_key = ? AND icv_reservation_token = ?
	`, booking.BranchID, booking.RequestKey, booking.EventID); err != nil {
		return fmt.Errorf("release booking of branch %d: %w", booking.BranchID, err)
	}
	return nil
}

// Commit stores the signed invoice and advances the chain in one transaction. The invoice hash becomes the previous hash of
// the next invoice, and the booking is dropped in the same commit.
func (c *SQLChain) Commit(ctx context.Context, booking Booking, invoice SignedInvoice) error {
	if invoice.BranchID != booking.BranchID || invoice.ICV != booking.ICV || invoice.UUID != booking.UUID || invoice.PIH != booking.PIH {
		return fmt.Errorf("%w: the invoice does not match its booking", ErrChainConflict)
	}
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin invoice commit: %w", err)
	}
	defer tx.Rollback()

	row, err := loadChainRow(ctx, tx, booking.BranchID, false)
	if err != nil {
		return fmt.Errorf("lock branch %d chain: %w", booking.BranchID, err)
	}
	if !row.holds(booking) {
		return fmt.Errorf("%w: branch %d", ErrChainConflict, booking.BranchID)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO zatca_submission
			(bill_id, branch_id, status, outcome_state, invoice_icv, previous_invoice_hash,
			 invoice_uuid, invoice_hash, invoice_kind, signed_xml)
		VALUES (?, ?, 0, 'pending', ?, ?, ?, ?, ?, ?)
	`, invoice.SourceID, invoice.BranchID, invoice.ICV, invoice.PIH,
		invoice.UUID, invoice.Hash, invoice.Kind, invoice.SignedXML); err != nil {
		return fmt.Errorf("store invoice of bill %d: %w", invoice.SourceID, err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE bill SET invoice_uuid = ?, invoice_hash = ?, qr_code = ? WHERE id = ?
	`, invoice.UUID, invoice.Hash, invoice.QR, invoice.SourceID); err != nil {
		return fmt.Errorf("record invoice fields of bill %d: %w", invoice.SourceID, err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE branch_zatca_config
		SET last_icv = ?, previous_invoice_hash = ?,
		    icv_reservation_key = NULL, icv_reservation_token = NULL, reserved_icv = NULL, reservation_lease_expires_at = NULL
		WHERE branch_id = ?
	`, invoice.ICV, invoice.Hash, booking.BranchID); err != nil {
		return fmt.Errorf("advance chain of branch %d: %w", booking.BranchID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit invoice of bill %d: %w", invoice.SourceID, err)
	}
	return nil
}

// Stored returns the invoice committed for a bill, or nil while the bill has none. The QR code is read from the bill, where
// Commit wrote it.
func (c *SQLChain) Stored(ctx context.Context, msg Message) (*SignedInvoice, error) {
	var (
		invoice SignedInvoice
		qr      sql.NullString
	)
	err := c.DB.QueryRowContext(ctx, `
		SELECT s.bill_id, s.branch_id, s.invoice_icv, s.previous_invoice_hash, s.invoice_uuid, s.invoice_hash,
		       s.invoice_kind, s.signed_xml, b.qr_code
		FROM zatca_submission s
		JOIN bill b ON b.id = s.bill_id
		WHERE s.bill_id = ?
	`, msg.ID).Scan(
		&invoice.SourceID, &invoice.BranchID, &invoice.ICV, &invoice.PIH,
		&invoice.UUID, &invoice.Hash, &invoice.Kind, &invoice.SignedXML, &qr,
	)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("lookup invoice of bill %d: %w", msg.ID, err)
	}
	invoice.QR = qr.String
	return &invoice, nil
}

// Record stores ZATCA's outcome for an invoice and appends it to the outcome log. A bill that ZATCA accepted or warned is
// marked issued. A rejected bill keeps its state, and the rejection is kept with it.
func (c *SQLChain) Record(ctx context.Context, invoice SignedInvoice, outcome Outcome) error {
	status, err := submissionStatus(outcome.State)
	if err != nil {
		return err
	}
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin outcome record: %w", err)
	}
	defer tx.Rollback()

	var submissionID int64
	if err := tx.QueryRowContext(ctx, `
		SELECT id FROM zatca_submission WHERE bill_id = ? FOR UPDATE
	`, invoice.SourceID).Scan(&submissionID); err != nil {
		return fmt.Errorf("lock submission of bill %d: %w", invoice.SourceID, err)
	}

	providerCode := nullString(outcome.ProviderCode, 64)
	var rejectionCode, rejectionMsg, warningMsg sql.NullString
	if outcome.State == OutcomeRejected {
		rejectionCode = providerCode
		rejectionMsg = nullString(outcome.Message, 512)
	} else {
		warningMsg = nullString(outcome.Message, 1024)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE zatca_submission
		SET status = ?, outcome_state = ?, provider_code = ?, rejection_code = ?, rejection_msg = ?,
		    provider_warning_message = ?, submitted_at = CURRENT_TIMESTAMP, outcome_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`, status, outcome.State, providerCode, rejectionCode, rejectionMsg, warningMsg, submissionID); err != nil {
		return fmt.Errorf("record outcome of bill %d: %w", invoice.SourceID, err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO zatca_submission_outcome
			(submission_id, outcome_state, provider_code, provider_warning_message, observed_at)
		VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP)
	`, submissionID, outcome.State, providerCode, warningMsg); err != nil {
		return fmt.Errorf("append outcome of bill %d: %w", invoice.SourceID, err)
	}
	if outcome.State != OutcomeRejected {
		if _, err := tx.ExecContext(ctx, `UPDATE bill SET state = 3 WHERE id = ?`, invoice.SourceID); err != nil {
			return fmt.Errorf("mark bill %d issued: %w", invoice.SourceID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit outcome of bill %d: %w", invoice.SourceID, err)
	}
	return nil
}

// submissionStatus is the numeric status kept beside outcome_state (goose 00003): 2 accepted, 3 rejected, 4 warned.
func submissionStatus(state string) (int, error) {
	switch state {
	case OutcomeAccepted:
		return 2, nil
	case OutcomeRejected:
		return 3, nil
	case OutcomeWarning:
		return 4, nil
	}
	return 0, fmt.Errorf("unknown outcome state %q", state)
}
