package processor

import (
	"database/sql"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/zatca-go/zatca/invoice"
	"github.com/zatca-go/zatca/zatca"
)

// signSubmitUpdate keeps the existing runtime entry point while routing the
// work through the fakeable provider and durable T003 store.
func signSubmitUpdate(
	db *sql.DB,
	svc *zatca.Service,
	inv *invoice.Invoice,
	rowID int64,
	table string,
	meta xmlMeta,
) (string, error) {
	meta.table = table
	if meta.documentKind == "" {
		meta.documentKind = table
	}
	if meta.sourceBillID == 0 && table == "bill" {
		meta.sourceBillID = rowID
	}
	return signSubmitUpdateWithProvider(
		newServiceSubmissionProvider(svc),
		newSQLSubmissionStore(db),
		inv,
		rowID,
		meta,
	)
}

func signSubmitUpdateWithProvider(
	provider SubmissionProvider,
	store submissionStore,
	inv *invoice.Invoice,
	rowID int64,
	meta xmlMeta,
) (string, error) {
	if inv == nil {
		return "", fmt.Errorf("invoice is nil")
	}
	if store == nil {
		return "", fmt.Errorf("submission store is nil")
	}

	startedAt := time.Now()
	attempt, err := store.StartSubmission(
		submissionMeta{
			merchantID:        meta.merchantID,
			sourceBillID:      meta.sourceBillID,
			documentKind:      meta.documentKind,
			stableDocumentKey: meta.stableDocumentKey,
			eventID:           meta.eventID,
			outboxID:          meta.outboxID,
			attempt:           meta.attempt,
			attemptCount:      meta.attemptCount,
			attemptID:         meta.attemptID,
			retryAttempt:      meta.retryAttempt,
			deadLetter:        meta.deadLetter,
		},
		inv.UUID,
		startedAt,
	)
	if err != nil {
		return "", err
	}
	if attempt.resolved {
		if attempt.invoiceHash != "" {
			return attempt.invoiceHash, nil
		}
		return inv.UUID, nil
	}
	if attempt.terminal {
		return "", &ProviderError{
			State:     attempt.outcomeState,
			Code:      attempt.errorCode,
			Message:   attempt.errorMessage,
			Retryable: false,
		}
	}
	if attempt.inFlight {
		return "", &DuplicateDeliveryError{DocumentID: inv.UUID}
	}

	update := documentUpdate{
		MerchantID:  meta.merchantID,
		Table:       meta.table,
		ID:          rowID,
		InvoiceUUID: inv.UUID,
	}
	outcome := submissionOutcome{
		State:      OutcomeTerminalFailure,
		RetryCount: attempt.attemptNumber - 1,
		Retryable:  false,
	}
	var artifacts []artifactRecord
	fail := func(processErr error, code string) (string, error) {
		terminalErr := &ProviderError{
			State:     OutcomeTerminalFailure,
			Code:      code,
			Message:   limitText(processErr.Error(), 1024),
			Err:       processErr,
			Retryable: false,
		}
		outcome.State = OutcomeTerminalFailure
		outcome.ErrorCode = code
		outcome.ErrorMessage = terminalErr.Message
		return "", finishSubmission(store, attempt, outcome, update, artifacts, terminalErr)
	}

	if provider == nil {
		return fail(fmt.Errorf("submission provider is nil"), "PROVIDER_NOT_CONFIGURED")
	}

	xmlStr, err := inv.ToXML()
	if err != nil {
		return fail(fmt.Errorf("ToXML failed: %w", err), "XML_GENERATION_FAILED")
	}

	signedXML, hash, err := provider.SignInvoice(xmlStr)
	if err != nil {
		return fail(fmt.Errorf("SignInvoice failed: %w", err), "SIGNING_FAILED")
	}
	if strings.TrimSpace(signedXML) == "" {
		return fail(fmt.Errorf("SignInvoice returned empty XML"), "EMPTY_SIGNED_XML")
	}
	if err := validateXMLContent(signedXML); err != nil {
		return fail(err, "INVALID_SIGNED_XML")
	}

	requestPath, requestArtifact, err := saveXMLArtifact(meta, rowID, "", signedXML)
	if err != nil {
		return fail(err, "REQUEST_ARTIFACT_FAILED")
	}
	requestArtifact.Metadata = eventArtifactMetadata(meta)
	if requestArtifact.Path != "" {
		artifacts = append(artifacts, requestArtifact)
	}
	outcome.RequestXMLPath = requestPath
	update.InvoiceHash = hash
	update.XMLPath = requestPath

	req, err := provider.GenerateAPIRequest(signedXML)
	if err != nil {
		return fail(fmt.Errorf("GenerateAPIRequest failed: %w", err), "REQUEST_GENERATION_FAILED")
	}
	if req.UUID == "" {
		req.UUID = inv.UUID
	}
	if req.UUID != inv.UUID {
		return fail(
			fmt.Errorf("provider request UUID %q does not match stable document UUID %q", req.UUID, inv.UUID),
			"DOCUMENT_IDENTITY_MISMATCH",
		)
	}
	if req.InvoiceHash == "" {
		req.InvoiceHash = hash
	}
	if req.InvoiceHash == "" {
		return fail(fmt.Errorf("provider request has no invoice hash"), "MISSING_INVOICE_HASH")
	}
	update.InvoiceHash = req.InvoiceHash
	update.InvoiceUUID = req.UUID

	qr := ""
	if generatedQR, qrErr := provider.GenerateQR(signedXML); qrErr == nil {
		qr = generatedQR
	} else {
		log.Printf("DEBUG: GenerateQR failed: %v", qrErr)
	}
	update.QR = qr

	finishProviderError := func(providerErr error, providerCalled bool) (string, error) {
		normalizedErr := normalizeProviderError(providerErr)
		outcome = providerErrorOutcome(normalizedErr)
		outcome.ProviderCalled = providerCalled
		outcome.RequestXMLPath = requestPath
		outcome.RetryCount = attempt.attemptNumber - 1
		if meta.deadLetter {
			outcome = terminalizeRetryExhaustion(outcome)
			normalizedErr = terminalizeProviderError(normalizedErr)
		}
		outcome.NextRetryAt = outcomeRetryTime(startedAt, outcome.Retryable, attempt.attemptNumber)
		update.InvoiceHash = req.InvoiceHash
		update.InvoiceUUID = req.UUID
		update.QR = qr
		update.XMLPath = requestPath
		logOutcome(outcome)
		return "", finishSubmission(store, attempt, outcome, update, artifacts, normalizedErr)
	}

	var (
		result         *ProviderResult
		providerCalled bool
		reconciled     bool
	)
	if attempt.needsReconciliation {
		reconciler, ok := provider.(SubmissionReconciler)
		if !ok {
			return finishProviderError(&ProviderError{
				State:   OutcomeAmbiguous,
				Code:    "RECONCILIATION_UNSUPPORTED",
				Message: "provider does not support submission reconciliation",
			}, false)
		}
		identity := ProviderIdentity{
			DocumentID:  req.UUID,
			InvoiceHash: req.InvoiceHash,
			ProviderRef: attempt.providerRef,
			StableKey:   meta.stableDocumentKey,
		}
		if attempt.invoiceHash != "" && attempt.invoiceHash != req.InvoiceHash {
			return finishProviderError(&ProviderError{
				State:   OutcomeTerminalFailure,
				Code:    "DOCUMENT_CONTENT_MISMATCH",
				Message: "stable provider identity has a different invoice hash",
			}, false)
		}
		result, err = reconciler.Reconcile(identity, inv.IsStandard())
		if err != nil {
			return finishProviderError(err, false)
		}
		if result != nil {
			reconciled = true
			providerCalled = true
		} else {
			starter, ok := store.(submissionRetryStarter)
			if !ok {
				return finishProviderError(&ProviderError{
					State:   OutcomeAmbiguous,
					Code:    "RECONCILIATION_NOT_FOUND",
					Message: "provider identity was not found and the store cannot allocate a retry attempt",
				}, false)
			}
			attempt, err = starter.StartRetrySubmission(
				submissionMeta{
					merchantID:        meta.merchantID,
					sourceBillID:      meta.sourceBillID,
					documentKind:      meta.documentKind,
					stableDocumentKey: meta.stableDocumentKey,
					eventID:           meta.eventID,
					outboxID:          meta.outboxID,
					attempt:           meta.attempt,
					attemptCount:      meta.attemptCount,
					attemptID:         meta.attemptID,
					retryAttempt:      meta.retryAttempt,
					deadLetter:        meta.deadLetter,
				},
				req.UUID,
				startedAt,
			)
			if err != nil {
				return "", err
			}
			if attempt.resolved {
				if attempt.invoiceHash != "" {
					return attempt.invoiceHash, nil
				}
				return req.InvoiceHash, nil
			}
			if attempt.inFlight {
				return "", &DuplicateDeliveryError{DocumentID: req.UUID}
			}
		}
	}

	if !reconciled {
		result, err = provider.Submit(req, inv.IsStandard())
		providerCalled = true
	}
	if err != nil {
		return finishProviderError(err, providerCalled)
	}
	if result == nil {
		return fail(fmt.Errorf("invoice API returned nil response"), "NIL_PROVIDER_RESPONSE")
	}
	if !result.State.valid() {
		return fail(fmt.Errorf("provider returned invalid outcome state %q", result.State), "INVALID_PROVIDER_STATE")
	}

	clearedPath := ""
	if result.ClearedXML != "" {
		var clearedArtifact artifactRecord
		clearedPath, clearedArtifact, err = saveXMLArtifact(meta, rowID, "-cleared", result.ClearedXML)
		if err != nil {
			return fail(err, "CLEARED_ARTIFACT_FAILED")
		}
		if clearedArtifact.Path != "" {
			artifacts = append(artifacts, clearedArtifact)
		}
	}

	finalHash := req.InvoiceHash
	if finalHash == "" {
		finalHash = result.InvoiceHash
	}
	if finalHash == "" {
		finalHash = hash
	}
	if finalHash == "" {
		return fail(fmt.Errorf("no invoice hash returned for %s %d", meta.table, rowID), "MISSING_INVOICE_HASH")
	}

	outcome = submissionOutcome{
		State:            result.State,
		ProviderCode:     limitText(result.ProviderCode, 64),
		WarningCode:      limitText(result.WarningCode, 64),
		WarningMessage:   limitText(result.WarningMessage, 1024),
		ProviderRef:      limitText(result.ProviderRef, 255),
		RejectionCode:    limitText(result.RejectionCode, 32),
		RejectionMessage: limitText(result.RejectionMessage, 512),
		RetryCount:       attempt.attemptNumber - 1,
		Retryable:        result.State == OutcomePending || result.State == OutcomeAmbiguous,
		ProviderCalled:   providerCalled,
		RequestXMLPath:   requestPath,
		ClearedXMLPath:   clearedPath,
	}
	if !result.State.success() {
		outcome.ErrorCode = outcome.RejectionCode
		outcome.ErrorMessage = outcome.RejectionMessage
		if outcome.ErrorCode == "" {
			outcome.ErrorCode = outcome.ProviderCode
		}
		if outcome.ErrorMessage == "" {
			outcome.ErrorMessage = fmt.Sprintf("provider outcome is %s", outcome.State)
		}
	}
	if meta.deadLetter {
		outcome = terminalizeRetryExhaustion(outcome)
	}
	outcome.NextRetryAt = outcomeRetryTime(startedAt, outcome.Retryable, attempt.attemptNumber)
	update.InvoiceHash = finalHash
	update.InvoiceUUID = req.UUID
	update.QR = qr
	update.XMLPath = requestPath
	update.MarkIssued = result.State.success()
	logOutcome(outcome)
	if err := finishSubmission(store, attempt, outcome, update, artifacts, nil); err != nil {
		return "", err
	}
	return finalHash, nil
}

func limitText(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
