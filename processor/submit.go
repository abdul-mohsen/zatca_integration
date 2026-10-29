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
			merchantID:   meta.merchantID,
			sourceBillID: meta.sourceBillID,
			documentKind: meta.documentKind,
		},
		inv.UUID,
		startedAt,
	)
	if err != nil {
		return "", err
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
	}
	var artifacts []artifactRecord
	fail := func(processErr error, code string) (string, error) {
		outcome.State = OutcomeTerminalFailure
		outcome.ErrorCode = code
		outcome.ErrorMessage = limitText(processErr.Error(), 1024)
		return "", finishSubmission(store, attempt, outcome, update, artifacts, processErr)
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

	requestPath, requestArtifact, err := saveXMLArtifact(meta, rowID, "", signedXML)
	if err != nil {
		return fail(err, "REQUEST_ARTIFACT_FAILED")
	}
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

	result, err := provider.Submit(req, inv.IsStandard())
	if err != nil {
		outcome = providerErrorOutcome(err)
		outcome.ProviderCalled = true
		outcome.RequestXMLPath = requestPath
		outcome.RetryCount = attempt.attemptNumber - 1
		outcome.NextRetryAt = outcomeRetryTime(startedAt, outcome.State, attempt.attemptNumber)
		update.InvoiceHash = req.InvoiceHash
		update.InvoiceUUID = req.UUID
		update.QR = qr
		update.XMLPath = requestPath
		logOutcome(outcome)
		return "", finishSubmission(store, attempt, outcome, update, artifacts, err)
	}
	if result == nil {
		return fail(fmt.Errorf("invoice API returned nil response"), "NIL_PROVIDER_RESPONSE")
	}
	if !result.State.valid() {
		return fail(fmt.Errorf("provider returned invalid outcome state %q", result.State), "INVALID_PROVIDER_STATE")
	}

	responseXML := result.ResponseXML
	if strings.TrimSpace(responseXML) == "" && inv.IsStandard() {
		responseXML = result.ClearedXML
	}

	responsePayload := result.ProviderResponse
	responseContentType := result.ProviderResponseType
	if strings.TrimSpace(responsePayload) == "" && strings.TrimSpace(responseXML) != "" {
		responsePayload = responseXML
		responseContentType = "application/xml"
	}
	if strings.TrimSpace(responsePayload) != "" && responseContentType == "" {
		responseContentType = "application/json"
	}

	clearedPath := ""
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
		NextRetryAt:      outcomeRetryTime(startedAt, result.State, attempt.attemptNumber),
		ProviderCalled:   true,
		RequestXMLPath:   requestPath,
		ResponseXML:      responseXML,
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
	artifactFailure := func(processErr error, code string) (string, error) {
		failedOutcome := outcome
		failedOutcome.State = OutcomeTerminalFailure
		failedOutcome.ErrorCode = code
		failedOutcome.ErrorMessage = limitText(processErr.Error(), 1024)
		failedOutcome.NextRetryAt = nil
		update.MarkIssued = false
		logOutcome(failedOutcome)
		return "", finishSubmission(store, attempt, failedOutcome, update, artifacts, processErr)
	}

	if result.ClearedXML != "" {
		var clearedArtifact artifactRecord
		clearedPath, clearedArtifact, err = saveXMLArtifact(meta, rowID, "-cleared", result.ClearedXML)
		if err != nil {
			return artifactFailure(err, "CLEARED_ARTIFACT_FAILED")
		}
		if clearedArtifact.Path != "" {
			artifacts = append(artifacts, clearedArtifact)
		}
	}

	if responsePayload != "" {
		var responseArtifact artifactRecord
		_, responseArtifact, err = saveArtifact(
			meta,
			rowID,
			"-response",
			responsePayload,
			"response_xml",
			responseContentType,
		)
		if err != nil {
			return artifactFailure(err, "RESPONSE_ARTIFACT_FAILED")
		}
		if responseArtifact.Path != "" {
			artifacts = append(artifacts, responseArtifact)
		}
	}

	outcome.ClearedXMLPath = clearedPath
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
