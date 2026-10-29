package processor

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type submissionMeta struct {
	merchantID   int64
	sourceBillID int64
	documentKind string
}

type submissionAttempt struct {
	submissionID  int64
	attemptID     int64
	attemptNumber int
	merchantID    int64
	startedAt     time.Time
}

type submissionOutcome struct {
	State            OutcomeState
	ProviderCode     string
	WarningCode      string
	WarningMessage   string
	ProviderRef      string
	RejectionCode    string
	RejectionMessage string
	ErrorCode        string
	ErrorMessage     string
	RetryCount       int
	NextRetryAt      *time.Time
	ProviderCalled   bool
	RequestXMLPath   string
	ClearedXMLPath   string
	ResponseXML      string
}

type documentUpdate struct {
	MerchantID  int64
	Table       string
	ID          int64
	InvoiceHash string
	InvoiceUUID string
	QR          string
	XMLPath     string
	MarkIssued  bool
}

type artifactRecord struct {
	Kind        string
	Path        string
	ContentType string
	SHA256      string
	Metadata    []byte
}

type submissionStore interface {
	StartSubmission(meta submissionMeta, documentID string, startedAt time.Time) (submissionAttempt, error)
	CompleteSubmission(attempt submissionAttempt, outcome submissionOutcome, update documentUpdate, artifacts []artifactRecord) error
}

type sqlSubmissionStore struct {
	db *sql.DB
}

func newSQLSubmissionStore(db *sql.DB) submissionStore {
	return &sqlSubmissionStore{db: db}
}

func (s *sqlSubmissionStore) StartSubmission(meta submissionMeta, documentID string, startedAt time.Time) (submissionAttempt, error) {
	if s == nil || s.db == nil {
		return submissionAttempt{}, fmt.Errorf("durable submission store is not configured")
	}
	if err := validateSubmissionMeta(meta, documentID); err != nil {
		return submissionAttempt{}, err
	}
	if meta.documentKind != "bill" {
		return submissionAttempt{}, fmt.Errorf(
			"T003 schema compatibility: %s document persistence requires a document-kind/source key; zatca_submission is uniquely keyed by bill_id",
			meta.documentKind,
		)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return submissionAttempt{}, fmt.Errorf("begin ZATCA submission transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	res, err := tx.Exec(`
		INSERT INTO zatca_submission
			(bill_id, merchant_id, status, document_id, outcome_state, retry_count, last_attempt_at, next_retry_at)
		VALUES (?, ?, 0, ?, 'pending', 0, ?, NULL)
		ON DUPLICATE KEY UPDATE id = LAST_INSERT_ID(id)`,
		meta.sourceBillID, meta.merchantID, documentID, startedAt,
	)
	if err != nil {
		return submissionAttempt{}, fmt.Errorf("create or load ZATCA submission for bill %d: %w", meta.sourceBillID, err)
	}

	submissionID, err := res.LastInsertId()
	if err != nil {
		return submissionAttempt{}, fmt.Errorf("read ZATCA submission id for bill %d: %w", meta.sourceBillID, err)
	}
	if submissionID == 0 {
		if err := tx.QueryRow(
			`SELECT id FROM zatca_submission WHERE bill_id = ? AND merchant_id = ? FOR UPDATE`,
			meta.sourceBillID, meta.merchantID,
		).Scan(&submissionID); err != nil {
			return submissionAttempt{}, fmt.Errorf("find ZATCA submission for bill %d: %w", meta.sourceBillID, err)
		}
	}

	var storedMerchantID int64
	var storedDocumentID string
	if err := tx.QueryRow(
		`SELECT merchant_id, document_id FROM zatca_submission WHERE id = ? FOR UPDATE`,
		submissionID,
	).Scan(&storedMerchantID, &storedDocumentID); err != nil {
		return submissionAttempt{}, fmt.Errorf("verify ZATCA submission %d identity: %w", submissionID, err)
	}
	if storedMerchantID != meta.merchantID {
		return submissionAttempt{}, fmt.Errorf(
			"ZATCA submission %d tenant mismatch: stored merchant_id=%d, current merchant_id=%d",
			submissionID, storedMerchantID, meta.merchantID,
		)
	}
	if storedDocumentID != documentID {
		return submissionAttempt{}, fmt.Errorf(
			"ZATCA submission %d document mismatch: stored document_id=%q, current document_id=%q",
			submissionID, storedDocumentID, documentID,
		)
	}

	var attemptNumber int
	if err := tx.QueryRow(
		`SELECT COALESCE(MAX(attempt_number), 0) + 1 FROM zatca_submission_attempt WHERE submission_id = ? FOR UPDATE`,
		submissionID,
	).Scan(&attemptNumber); err != nil {
		return submissionAttempt{}, fmt.Errorf("allocate ZATCA attempt for submission %d: %w", submissionID, err)
	}
	if _, err := tx.Exec(`
		UPDATE zatca_submission
		SET status = 0,
			outcome_state = 'pending',
			provider_code = NULL,
			provider_warning_code = NULL,
			provider_warning_message = NULL,
			provider_reference = NULL,
			rejection_code = NULL,
			rejection_msg = NULL,
			retry_count = ?,
			last_attempt_at = ?,
			next_retry_at = NULL,
			outcome_at = NULL
		WHERE id = ? AND merchant_id = ?`,
		attemptNumber-1, startedAt, submissionID, meta.merchantID,
	); err != nil {
		return submissionAttempt{}, fmt.Errorf("mark ZATCA submission %d pending: %w", submissionID, err)
	}

	res, err = tx.Exec(`
		INSERT INTO zatca_submission_attempt
			(submission_id, attempt_number, attempt_state, retry_count, started_at)
		VALUES (?, ?, 'pending', ?, ?)`,
		submissionID, attemptNumber, attemptNumber-1, startedAt,
	)
	if err != nil {
		return submissionAttempt{}, fmt.Errorf("create ZATCA attempt %d for submission %d: %w", attemptNumber, submissionID, err)
	}
	attemptID, err := res.LastInsertId()
	if err != nil {
		return submissionAttempt{}, fmt.Errorf("read ZATCA attempt id for submission %d: %w", submissionID, err)
	}

	if _, err := tx.Exec(`
		INSERT INTO zatca_submission_outcome
			(submission_id, attempt_id, outcome_state, observed_at)
		VALUES (?, ?, 'pending', ?)`,
		submissionID, attemptID, startedAt,
	); err != nil {
		return submissionAttempt{}, fmt.Errorf("record pending ZATCA outcome for submission %d: %w", submissionID, err)
	}

	if err = tx.Commit(); err != nil {
		return submissionAttempt{}, fmt.Errorf("commit pending ZATCA submission %d: %w", submissionID, err)
	}
	committed = true
	return submissionAttempt{
		submissionID:  submissionID,
		attemptID:     attemptID,
		attemptNumber: attemptNumber,
		merchantID:    meta.merchantID,
		startedAt:     startedAt,
	}, nil
}

func (s *sqlSubmissionStore) CompleteSubmission(
	attempt submissionAttempt,
	outcome submissionOutcome,
	update documentUpdate,
	artifacts []artifactRecord,
) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("durable submission store is not configured")
	}
	if !outcome.State.valid() {
		return fmt.Errorf("invalid ZATCA outcome state %q", outcome.State)
	}
	if err := validateDocumentUpdate(update); err != nil {
		return err
	}
	if update.MerchantID != attempt.merchantID {
		return fmt.Errorf(
			"ZATCA document update tenant mismatch: submission merchant_id=%d, document merchant_id=%d",
			attempt.merchantID, update.MerchantID,
		)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin ZATCA outcome transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	status := legacySubmissionStatus(outcome.State)
	var nextRetryAt any
	if outcome.NextRetryAt != nil {
		nextRetryAt = *outcome.NextRetryAt
	}
	var submittedAt any
	if outcome.ProviderCalled {
		submittedAt = time.Now()
	}
	var clearedAt any
	if outcome.ClearedXMLPath != "" {
		clearedAt = time.Now()
	}

	if _, err := tx.Exec(`
		UPDATE zatca_submission
		SET status = ?,
			outcome_state = ?,
			provider_code = ?,
			provider_warning_code = ?,
			provider_warning_message = ?,
			provider_reference = ?,
			rejection_code = ?,
			rejection_msg = ?,
			submitted_at = ?,
			cleared_at = ?,
			retry_count = ?,
			last_attempt_at = ?,
			next_retry_at = ?,
			outcome_at = ?,
			request_xml_path = ?,
			cleared_xml_path = ?,
			response_xml = ?
		WHERE id = ? AND merchant_id = ?`,
		status,
		outcome.State,
		nullableText(outcome.ProviderCode),
		nullableText(outcome.WarningCode),
		nullableText(outcome.WarningMessage),
		nullableText(outcome.ProviderRef),
		nullableText(outcome.RejectionCode),
		nullableText(outcome.RejectionMessage),
		submittedAt,
		clearedAt,
		outcome.RetryCount,
		attempt.startedAt,
		nextRetryAt,
		time.Now(),
		nullableText(outcome.RequestXMLPath),
		nullableText(outcome.ClearedXMLPath),
		nullableText(outcome.ResponseXML),
		attempt.submissionID,
		attempt.merchantID,
	); err != nil {
		return fmt.Errorf("update ZATCA submission %d outcome: %w", attempt.submissionID, err)
	}

	if _, err := tx.Exec(`
		UPDATE zatca_submission_attempt
		SET attempt_state = ?,
			provider_code = ?,
			provider_warning_code = ?,
			provider_warning_message = ?,
			provider_reference = ?,
			error_code = ?,
			error_message = ?,
			retry_count = ?,
			next_retry_at = ?,
			request_xml_path = ?,
			cleared_xml_path = ?,
			completed_at = ?
		WHERE id = ? AND submission_id = ?`,
		outcome.State,
		nullableText(outcome.ProviderCode),
		nullableText(outcome.WarningCode),
		nullableText(outcome.WarningMessage),
		nullableText(outcome.ProviderRef),
		nullableText(outcome.ErrorCode),
		nullableText(outcome.ErrorMessage),
		outcome.RetryCount,
		nextRetryAt,
		nullableText(outcome.RequestXMLPath),
		nullableText(outcome.ClearedXMLPath),
		time.Now(),
		attempt.attemptID,
		attempt.submissionID,
	); err != nil {
		return fmt.Errorf("close ZATCA attempt %d: %w", attempt.attemptID, err)
	}

	if _, err := tx.Exec(`
		INSERT INTO zatca_submission_outcome
			(submission_id, attempt_id, outcome_state, provider_code,
			 provider_warning_code, provider_warning_message, provider_reference, observed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		attempt.submissionID,
		attempt.attemptID,
		outcome.State,
		nullableText(outcome.ProviderCode),
		nullableText(outcome.WarningCode),
		nullableText(outcome.WarningMessage),
		nullableText(outcome.ProviderRef),
		time.Now(),
	); err != nil {
		return fmt.Errorf("record ZATCA outcome for submission %d: %w", attempt.submissionID, err)
	}

	for _, artifact := range artifacts {
		if strings.TrimSpace(artifact.Kind) == "" {
			return fmt.Errorf("record ZATCA artifact for submission %d: artifact kind is empty", attempt.submissionID)
		}
		if strings.TrimSpace(artifact.Path) == "" {
			return fmt.Errorf("record ZATCA artifact %s for submission %d: artifact path is empty", artifact.Kind, attempt.submissionID)
		}
		if _, err := tx.Exec(`
			INSERT INTO zatca_submission_artifact
				(submission_id, attempt_id, artifact_kind, artifact_path, content_type, sha256, metadata)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			attempt.submissionID,
			attempt.attemptID,
			artifact.Kind,
			artifact.Path,
			nullableText(artifact.ContentType),
			nullableText(artifact.SHA256),
			nullableJSON(artifact.Metadata),
		); err != nil {
			return fmt.Errorf("record ZATCA artifact %s for submission %d: %w", artifact.Kind, attempt.submissionID, err)
		}
	}

	if _, err := tx.Exec(documentUpdateSQL(update), update.InvoiceHash, update.QR, update.InvoiceUUID, nullableText(update.XMLPath), update.ID, update.MerchantID); err != nil {
		return fmt.Errorf("update %s %d ZATCA metadata: %w", update.Table, update.ID, err)
	}

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit ZATCA outcome for submission %d: %w", attempt.submissionID, err)
	}
	committed = true
	return nil
}

func validateSubmissionMeta(meta submissionMeta, documentID string) error {
	if meta.merchantID <= 0 {
		return fmt.Errorf("ZATCA submission requires a positive merchant_id")
	}
	if meta.sourceBillID <= 0 {
		return fmt.Errorf("ZATCA submission requires a positive bill_id")
	}
	if strings.TrimSpace(documentID) == "" {
		return fmt.Errorf("ZATCA submission requires a stable document_id")
	}
	if meta.documentKind == "" {
		return fmt.Errorf("ZATCA submission requires a document kind")
	}
	return nil
}

func validateDocumentUpdate(update documentUpdate) error {
	if update.MerchantID <= 0 {
		return fmt.Errorf("ZATCA document update requires a positive merchant_id")
	}
	switch update.Table {
	case "bill":
		if update.ID <= 0 {
			return fmt.Errorf("ZATCA document update requires a positive bill id")
		}
	default:
		return fmt.Errorf(
			"T003 schema compatibility: durable document updates for table %q are unsupported; only bill is keyed by zatca_submission.bill_id",
			update.Table,
		)
	}
	return nil
}

func documentUpdateSQL(update documentUpdate) string {
	query := `UPDATE bill
		SET invoice_hash = ?, qr_code = ?, invoice_uuid = ?, invoice_xml_path = ?`
	if update.MarkIssued {
		query += `, state = 3`
	}
	return query + ` WHERE id = ? AND merchant_id = ?`
}

func legacySubmissionStatus(state OutcomeState) int {
	switch state {
	case OutcomeAccepted:
		return 2
	case OutcomeAcceptedWithWarning:
		return 4
	case OutcomeRejected:
		return 3
	default:
		return 0
	}
}

func nullableText(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}

func nullableJSON(data []byte) any {
	if len(data) == 0 {
		return nil
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return nil
	}
	return data
}

func saveXMLArtifact(meta xmlMeta, rowID int64, suffix, content string) (string, artifactRecord, error) {
	kind := "request_xml"
	if suffix != "" {
		kind = "cleared_xml"
	}
	return saveArtifact(meta, rowID, suffix, content, kind, "application/xml")
}

func saveArtifact(
	meta xmlMeta,
	rowID int64,
	suffix string,
	content string,
	kind string,
	contentType string,
) (string, artifactRecord, error) {
	if strings.TrimSpace(meta.xmlDir) == "" {
		return "", artifactRecord{}, fmt.Errorf("%s artifact store is not configured", kind)
	}
	dir := filepath.Join(
		meta.xmlDir,
		tenantPath(meta.tenantID),
		fmt.Sprintf("%d", meta.companyID),
		fmt.Sprintf("%d", meta.branchID),
		meta.billType,
	)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", artifactRecord{}, fmt.Errorf("create %s artifact directory %s: %w", kind, dir, err)
	}
	extension := ".xml"
	if contentType == "application/json" {
		extension = ".json"
	}
	name := fmt.Sprintf("%d%s%s", rowID, suffix, extension)
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return "", artifactRecord{}, fmt.Errorf("write %s artifact %s: %w", kind, path, err)
	}
	return path, artifactRecord{
		Kind:        kind,
		Path:        path,
		ContentType: contentType,
		SHA256:      sha256Hex([]byte(content)),
	}, nil
}

func tenantPath(tenantID string) string {
	if tenantID == "" {
		return "_default"
	}
	return tenantID
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func outcomeRetryTime(now time.Time, state OutcomeState, attemptNumber int) *time.Time {
	if state == OutcomeTerminalFailure || state.success() {
		return nil
	}
	if attemptNumber < 1 {
		attemptNumber = 1
	}
	delay := time.Duration(1<<min(attemptNumber, 8)) * time.Second
	if delay > 5*time.Minute {
		delay = 5 * time.Minute
	}
	next := now.Add(delay)
	return &next
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func outcomeError(outcome submissionOutcome) error {
	message := outcome.ErrorMessage
	if message == "" {
		message = outcome.RejectionMessage
	}
	if message == "" {
		message = fmt.Sprintf("ZATCA outcome is %s", outcome.State)
	}
	return &ProviderError{
		State:   outcome.State,
		Code:    outcome.ErrorCode,
		Message: message,
	}
}

func providerErrorOutcome(err error) submissionOutcome {
	var providerErr *ProviderError
	if errors.As(err, &providerErr) {
		state := providerErr.State
		if !state.valid() {
			state = OutcomeAmbiguous
		}
		return submissionOutcome{
			State:        state,
			ProviderCode: providerErr.Code,
			ErrorCode:    providerErr.Code,
			ErrorMessage: providerErr.Message,
		}
	}
	return submissionOutcome{
		State:        OutcomeAmbiguous,
		ProviderCode: "TRANSPORT_ERROR",
		ErrorCode:    "TRANSPORT_ERROR",
		ErrorMessage: err.Error(),
	}
}

func finishSubmission(
	store submissionStore,
	attempt submissionAttempt,
	outcome submissionOutcome,
	update documentUpdate,
	artifacts []artifactRecord,
	originalErr error,
) error {
	if err := store.CompleteSubmission(attempt, outcome, update, artifacts); err != nil {
		if originalErr != nil {
			return fmt.Errorf("%v; persist ZATCA outcome: %w", originalErr, err)
		}
		return err
	}
	if originalErr != nil {
		return originalErr
	}
	if !outcome.State.success() {
		return outcomeError(outcome)
	}
	return nil
}

func logOutcome(outcome submissionOutcome) {
	if outcome.WarningCode != "" || outcome.WarningMessage != "" {
		log.Printf("  ZATCA warning [%s] %s", outcome.WarningCode, outcome.WarningMessage)
	}
}
