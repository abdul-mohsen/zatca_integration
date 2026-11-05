package processor

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/zatca-go/zatca/client"
	"github.com/zatca-go/zatca/config"
	"github.com/zatca-go/zatca/invoice"
	"github.com/zatca-go/zatca/models"
	"github.com/zatca-go/zatca/queue"
	"github.com/zatca-go/zatca/zatca"
)

// ServiceFunc returns the ZATCA service of a branch and the branch's configuration.
type ServiceFunc func(branchID int64) (*zatca.Service, *config.Config, error)

// BillPipeline is the fiscal pipeline of a sale. It verifies the bill, builds and signs its invoice under a booking, and
// submits the stored XML to ZATCA.
type BillPipeline struct {
	DB      *sql.DB
	Service ServiceFunc
}

// Prepare loads and verifies the bill. A bill that cannot be reported is refused here, before any number is booked.
func (p *BillPipeline) Prepare(_ context.Context, msg queue.Message) (queue.Prepared, error) {
	svc, cfg, err := p.Service(msg.BranchID)
	if err != nil {
		return nil, err
	}
	bill, err := QueryBillByID(p.DB, msg.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: bill %d does not exist", queue.ErrRejectedInput, msg.ID)
	}
	if err != nil {
		return nil, err
	}
	products, err := LoadProducts(p.DB, msg.ID)
	if err != nil {
		return nil, fmt.Errorf("load products failed for bill %d: %w", msg.ID, err)
	}
	if err := verifyBill(cfg, msg, *bill, products); err != nil {
		return nil, fmt.Errorf("%w: bill %d: %v", queue.ErrRejectedInput, msg.ID, err)
	}
	issueTime, err := ParseDates(bill.EffectiveDate, bill.PaymentDue, bill.ID)
	if err != nil {
		return nil, fmt.Errorf("%w: bill %d: %v", queue.ErrRejectedInput, msg.ID, err)
	}
	return &billDocument{svc: svc, cfg: cfg, bill: *bill, products: products, issueTime: issueTime}, nil
}

// Submit sends a committed invoice to ZATCA. A standard invoice is cleared, and a simplified one is reported. A rejection by
// ZATCA is an outcome, and so is an answer that needs reconciliation. Any other failure is technical, and the stored XML is
// sent again later.
func (p *BillPipeline) Submit(_ context.Context, invoice queue.SignedInvoice) (queue.Outcome, error) {
	svc, _, err := p.Service(invoice.BranchID)
	if err != nil {
		return queue.Outcome{}, err
	}
	apiReq, err := apiRequest(svc, invoice.SignedXML)
	if err != nil {
		return queue.Outcome{}, err
	}
	if apiReq.InvoiceHash != "" && apiReq.InvoiceHash != invoice.Hash {
		return queue.Outcome{}, fmt.Errorf("stored hash of bill %d does not match its signed XML", invoice.SourceID)
	}

	var resp *models.InvoiceResponse
	if invoice.Kind == queue.InvoiceStandard {
		resp, err = svc.Client.ClearInvoice(apiReq)
	} else {
		resp, err = svc.Client.ReportInvoice(apiReq)
	}
	if err != nil {
		return rejectionOutcome(invoice, err)
	}
	if resp == nil {
		return queue.Outcome{}, fmt.Errorf("ZATCA returned no response for bill %d", invoice.SourceID)
	}
	return responseOutcome(resp), nil
}

// responseOutcome classifies a 2xx answer. ZATCA can answer 2xx and still list
// error messages or a failed validation status, and those reject the invoice.
// Warnings, in the answer or in its validation results, accept it with a
// warning.
func responseOutcome(resp *models.InvoiceResponse) queue.Outcome {
	if vr := resp.ValidationResults; vr != nil && len(vr.ErrorMessages) > 0 {
		return queue.Outcome{State: queue.OutcomeRejected, ProviderCode: vr.ErrorMessages[0].Code, Message: validationText(vr.ErrorMessages)}
	}
	if len(resp.Errors) > 0 {
		return queue.Outcome{
			State:        queue.OutcomeRejected,
			ProviderCode: resp.Errors[0].Code,
			Message:      errorText(resp.Errors),
		}
	}
	if vr := resp.ValidationResults; vr != nil {
		switch strings.ToUpper(strings.TrimSpace(vr.Status)) {
		case "ERROR", "FAIL", "FAILED":
			return queue.Outcome{
				State:        queue.OutcomeRejected,
				ProviderCode: strings.ToUpper(strings.TrimSpace(vr.Status)),
				Message:      "provider validation status: " + strings.ToUpper(strings.TrimSpace(vr.Status)),
			}
		}
	}
	if len(resp.Warnings) > 0 {
		warning := resp.Warnings[0]
		return queue.Outcome{State: queue.OutcomeWarning, ProviderCode: warning.Code, Message: warning.Message}
	}
	if vr := resp.ValidationResults; vr != nil && len(vr.WarningMessages) > 0 {
		warning := vr.WarningMessages[0]
		return queue.Outcome{State: queue.OutcomeWarning, ProviderCode: warning.Code, Message: warning.Message}
	}
	if vr := resp.ValidationResults; vr != nil {
		switch strings.ToUpper(strings.TrimSpace(vr.Status)) {
		case "WARNING", "WARN":
			status := strings.ToUpper(strings.TrimSpace(vr.Status))
			return queue.Outcome{
				State:        queue.OutcomeWarning,
				ProviderCode: status,
				Message:      "provider validation status: " + status,
			}
		}
	}
	return queue.Outcome{State: queue.OutcomeAccepted}
}

// validationText joins the messages of a validation result into one text.
func validationText(messages []models.ValidationMessage) string {
	texts := make([]string, 0, len(messages))
	for _, m := range messages {
		texts = append(texts, m.Message)
	}
	return strings.Join(texts, "; ")
}

func errorText(messages []models.ErrorDetail) string {
	texts := make([]string, 0, len(messages))
	for _, m := range messages {
		texts = append(texts, m.Message)
	}
	return strings.Join(texts, "; ")
}

// rejectionOutcome classifies an error from ZATCA. A 409 means ZATCA already holds an invoice with this UUID, for example after
// a crash. That answer is ambiguous, not a rejection, and it needs reconciliation. A 4xx answer is a rejection of the invoice,
// except for authentication, timeouts, and rate limits, which are technical. Any other error is technical.
func rejectionOutcome(invoice queue.SignedInvoice, err error) (queue.Outcome, error) {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusConflict {
		return queue.Outcome{State: queue.OutcomeAmbiguous, ProviderCode: strconv.Itoa(apiErr.StatusCode), Message: apiErr.Body},
			fmt.Errorf("%w: bill %d (invoice %s): ZATCA already holds this invoice, so the outcome needs reconciliation",
				queue.ErrOutcomeAmbiguous, invoice.SourceID, invoice.UUID)
	}
	if errors.As(err, &apiErr) && isInvoiceRejection(apiErr.StatusCode) {
		return queue.Outcome{State: queue.OutcomeRejected, ProviderCode: strconv.Itoa(apiErr.StatusCode), Message: apiErr.Body}, nil
	}
	return queue.Outcome{}, fmt.Errorf("submit invoice to ZATCA: %w", err)
}

func isInvoiceRejection(status int) bool {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusRequestTimeout, http.StatusTooManyRequests:
		return false
	}
	return status >= http.StatusBadRequest && status < http.StatusInternalServerError
}

// billDocument is a verified bill, ready to be built under a booking.
type billDocument struct {
	svc       *zatca.Service
	cfg       *config.Config
	bill      BillRow
	products  []ProductRow
	issueTime time.Time
}

// Build builds the invoice with the booked UUID, ICV, and previous hash, then signs it and computes its hash and QR code.
// Nothing is sent to ZATCA here.
func (d *billDocument) Build(_ context.Context, booking queue.Booking) (queue.SignedInvoice, error) {
	inv := buildBillInvoice(d.cfg, d.bill, d.products, d.issueTime, booking)
	xmlStr, err := inv.ToXML()
	if err != nil {
		return queue.SignedInvoice{}, fmt.Errorf("build XML of bill %d: %w", d.bill.ID, err)
	}
	signedXML, hash, err := d.svc.SDK.SignInvoice(xmlStr)
	if err != nil {
		return queue.SignedInvoice{}, fmt.Errorf("sign bill %d: %w", d.bill.ID, err)
	}
	apiReq, err := apiRequest(d.svc, signedXML)
	if err != nil {
		return queue.SignedInvoice{}, err
	}
	if apiReq.UUID != booking.UUID {
		return queue.SignedInvoice{}, fmt.Errorf("signed bill %d carries UUID %s, not the booked %s",
			d.bill.ID, apiReq.UUID, booking.UUID)
	}
	invoiceHash := apiReq.InvoiceHash
	if invoiceHash == "" {
		invoiceHash = hash
	}
	if invoiceHash == "" {
		return queue.SignedInvoice{}, fmt.Errorf("signed bill %d has no invoice hash", d.bill.ID)
	}
	qr, err := d.svc.SDK.GenerateQR(signedXML)
	if err != nil {
		return queue.SignedInvoice{}, fmt.Errorf("QR code of bill %d: %w", d.bill.ID, err)
	}
	kind := queue.InvoiceSimplified
	if inv.IsStandard() {
		kind = queue.InvoiceStandard
	}
	return queue.SignedInvoice{
		SourceID:  d.bill.ID,
		BranchID:  booking.BranchID,
		UUID:      booking.UUID,
		ICV:       booking.ICV,
		PIH:       booking.PIH,
		Hash:      invoiceHash,
		Kind:      kind,
		SignedXML: signedXML,
		QR:        qr,
	}, nil
}

// verifyBill refuses a bill that cannot be reported, before any number is booked. The bill must belong to the envelope's branch,
// be a final sale, carry the envelope's invoice UUID when it has one, and have
// the branch/printed document identity that is emitted as XML cbc:ID. The
// seller's VAT and EGS id come from the branch configuration, and the bill
// needs at least one line with a positive quantity and a price that is not
// negative.
func verifyBill(cfg *config.Config, msg queue.Message, bill BillRow, products []ProductRow) error {
	switch {
	case bill.BranchID != msg.BranchID:
		return fmt.Errorf("the bill belongs to branch %d, not %d", bill.BranchID, msg.BranchID)
	case bill.State < 1:
		return fmt.Errorf("the bill is not a final sale (state %d)", bill.State)
	case bill.InvoiceUUID.Valid && bill.InvoiceUUID.String != msg.InvoiceUUID:
		return errors.New("the bill carries another invoice UUID")
	case billDocumentID(bill) == "":
		return errors.New("the bill has no branch sequence/printed document identity")
	case cfg.Seller.VAT == "":
		return errors.New("the branch has no seller VAT number")
	case cfg.EGSID == "":
		return errors.New("the branch has no EGS id: onboard the branch first")
	case len(products) == 0:
		return errors.New("the bill has no lines")
	}
	for i := range products {
		if products[i].QuantityOrZero() <= 0 {
			return fmt.Errorf("line %d has no positive quantity", i+1)
		}
		if products[i].PriceOrZero() < 0 {
			return fmt.Errorf("line %d has a negative price", i+1)
		}
	}
	return nil
}

func billDocumentID(b BillRow) string {
	if !b.SeqNumber.Valid {
		return ""
	}
	return strings.TrimSpace(b.SeqNumber.String)
}

// apiRequest derives the ZATCA request of a signed invoice. It is deterministic, so a stored XML gives the same request
// every time it is sent.
func apiRequest(svc *zatca.Service, signedXML string) (models.InvoiceRequest, error) {
	apiJSON, err := svc.SDK.GenerateAPIRequest(signedXML)
	if err != nil {
		return models.InvoiceRequest{}, fmt.Errorf("generate API request: %w", err)
	}
	var req models.InvoiceRequest
	if err := json.Unmarshal([]byte(apiJSON), &req); err != nil {
		return models.InvoiceRequest{}, fmt.Errorf("parse API request: %w", err)
	}
	return req, nil
}

// buildBillInvoice constructs the invoice (type 388) under a booking. It uses the booked UUID, ICV, and previous hash, and
// never chooses its own.
func buildBillInvoice(cfg *config.Config, b BillRow, products []ProductRow, issueTime time.Time, booking queue.Booking) *invoice.Invoice {
	isStandard := b.BuyerVAT.Valid && b.BuyerVAT.String != ""
	inv := &invoice.Invoice{
		ID:               billDocumentID(b),
		UUID:             booking.UUID,
		IssueDate:        issueTime,
		IssueTime:        issueTime,
		TypeCode:         invoice.TypeInvoice,
		DocumentCurrency: "SAR",
		TaxCurrency:      "SAR",
		Supplier: invoice.Party{
			RegistrationName:   cfg.Seller.Name,
			RegistrationNameAR: cfg.Seller.NameAR,
			VAT:                cfg.Seller.VAT,
			SchemeID:           cfg.Seller.CRN,
			ID:                 cfg.Seller.CRN,
			Street:             cfg.Seller.Street,
			Building:           cfg.Seller.Building,
			District:           cfg.Seller.District,
			City:               cfg.Seller.City,
			PostalCode:         cfg.Seller.PostalCode,
			Country:            cfg.Seller.Country,
		},
		Customer:            buildCustomer(b, isStandard),
		PreviousInvoiceHash: booking.PIH,
		InvoiceCounterValue: int(booking.ICV),
		PaymentMeans:        paymentMeans(b),
		Lines:               buildLines(products),
	}
	if isStandard {
		inv.SubType = invoice.SubTypeStandard
	} else {
		inv.SubType = invoice.SubTypeSimplified
	}
	return inv
}

func paymentMeans(b BillRow) string {
	if b.PaymentMethod.Valid && b.PaymentMethod.String != "" {
		return b.PaymentMethod.String
	}
	return invoice.PaymentCash
}

// buildCustomer builds the Customer party from BillRow.
func buildCustomer(b BillRow, isStandard bool) invoice.Party {
	customer := invoice.Party{
		RegistrationName: func() string {
			if b.UserName.Valid {
				return b.UserName.String
			}
			return "Customer"
		}(),
		Country: "SA",
	}

	if isStandard {
		customer.VAT = b.BuyerVAT.String
		if b.BuyerCompany.Valid && b.BuyerCompany.String != "" {
			customer.RegistrationName = b.BuyerCompany.String
		} else if b.BuyerName.Valid && b.BuyerName.String != "" {
			customer.RegistrationName = b.BuyerName.String
		}
		if b.BuyerStreet.Valid && b.BuyerStreet.String != "" {
			customer.Street = b.BuyerStreet.String
		} else if b.BuyerAddress.Valid && b.BuyerAddress.String != "" {
			customer.Street = b.BuyerAddress.String
		}
		if b.BuyerBuilding.Valid && b.BuyerBuilding.String != "" {
			customer.Building = b.BuyerBuilding.String
		}
		if b.BuyerDistrict.Valid && b.BuyerDistrict.String != "" {
			customer.District = b.BuyerDistrict.String
		}
		if b.BuyerCity.Valid && b.BuyerCity.String != "" {
			customer.City = b.BuyerCity.String
		}
		if b.BuyerPostalCode.Valid && b.BuyerPostalCode.String != "" {
			customer.PostalCode = b.BuyerPostalCode.String
		}
		if b.BuyerCountry.Valid && b.BuyerCountry.String != "" {
			customer.Country = b.BuyerCountry.String
		}
		if b.BuyerSchemeID.Valid && b.BuyerSchemeID.String != "" {
			customer.SchemeID = b.BuyerSchemeID.String
		}
		if b.BuyerRegistration.Valid && b.BuyerRegistration.String != "" {
			customer.ID = b.BuyerRegistration.String
		}
	}
	return customer
}

// buildLines converts ProductRows into invoice LineItems.
func buildLines(products []ProductRow) []invoice.LineItem {
	var lines []invoice.LineItem
	for i, p := range products {
		lineName := "product"
		if p.Name.Valid && p.Name.String != "" {
			lineName = p.Name.String
		} else if p.ProductID.Valid {
			lineName = fmt.Sprintf("product-%d", p.ProductID.Int64)
		}

		li := invoice.LineItem{
			ID:          fmt.Sprintf("%d", i+1),
			Name:        lineName,
			Quantity:    p.QuantityOrZero(),
			UnitCode:    "PCE",
			UnitPrice:   p.PriceOrZero(),
			Discount:    0,
			TaxCategory: invoice.TaxStandard,
			TaxPercent:  p.VATOrZero(),
		}
		lines = append(lines, li)
	}
	return lines
}
