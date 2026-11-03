package processor

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
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

// DocumentPipeline is the fiscal pipeline of the documents: a final sale, a credit note, and a debit note. It verifies the document,
// builds and signs its invoice under a booking, and submits the stored XML to ZATCA. The three kinds take the same steps. They differ in
// what they verify, and in the invoice they build.
type DocumentPipeline struct {
	DB      *sql.DB
	Service ServiceFunc
}

// Prepare loads and verifies the document. A document that cannot be reported is refused here, before any number is booked.
func (p *DocumentPipeline) Prepare(ctx context.Context, msg queue.Message) (queue.Prepared, error) {
	svc, cfg, err := p.Service(msg.BranchID)
	if err != nil {
		return nil, err
	}
	switch msg.DocType {
	case queue.DocBill:
		return p.prepareBill(msg, svc, cfg)
	case queue.DocCredit, queue.DocDebit:
		return p.prepareNote(ctx, msg, svc, cfg)
	}
	return nil, fmt.Errorf("%w: unsupported document type %q", queue.ErrRejectedInput, msg.DocType)
}

// prepareBill loads and verifies a final sale of the envelope's branch.
func (p *DocumentPipeline) prepareBill(msg queue.Message, svc *zatca.Service, cfg *config.Config) (queue.Prepared, error) {
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
	return &document{
		svc:      svc,
		docType:  queue.DocBill,
		sourceID: bill.ID,
		billID:   bill.ID,
		build: func(booking queue.Booking) *invoice.Invoice {
			return buildBillInvoice(cfg, *bill, products, issueTime, booking)
		},
	}, nil
}

// Submit sends a committed invoice to ZATCA. A standard invoice is cleared, and a simplified one is reported. A rejection by ZATCA is
// an outcome. Any other failure is technical, and the stored XML is sent again later.
func (p *DocumentPipeline) Submit(_ context.Context, invoice queue.SignedInvoice) (queue.Outcome, error) {
	svc, _, err := p.Service(invoice.BranchID)
	if err != nil {
		return queue.Outcome{}, err
	}
	apiReq, err := apiRequest(svc, invoice.SignedXML)
	if err != nil {
		return queue.Outcome{}, err
	}
	if apiReq.InvoiceHash != "" && apiReq.InvoiceHash != invoice.Hash {
		return queue.Outcome{}, fmt.Errorf("stored hash of %s %d does not match its signed XML", invoice.DocType, invoice.SourceID)
	}

	var resp *models.InvoiceResponse
	if invoice.Kind == queue.InvoiceStandard {
		resp, err = svc.Client.ClearInvoice(apiReq)
	} else {
		resp, err = svc.Client.ReportInvoice(apiReq)
	}
	if err != nil {
		return rejectionOutcome(err)
	}
	if resp == nil {
		return queue.Outcome{}, fmt.Errorf("ZATCA returned no response for %s %d", invoice.DocType, invoice.SourceID)
	}
	if len(resp.Warnings) > 0 {
		warning := resp.Warnings[0]
		return queue.Outcome{State: queue.OutcomeWarning, ProviderCode: warning.Code, Message: warning.Message}, nil
	}
	return queue.Outcome{State: queue.OutcomeAccepted}, nil
}

// rejectionOutcome classifies an error from ZATCA. A 4xx answer is a rejection of the invoice, except for authentication,
// timeouts, and rate limits, which are technical. Any other error is technical.
func rejectionOutcome(err error) (queue.Outcome, error) {
	var apiErr *client.APIError
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

// document is a verified document, ready to be built under a booking. Its build function makes the invoice, and nothing is sent to
// ZATCA while it is built.
type document struct {
	svc      *zatca.Service
	docType  queue.DocType
	sourceID int64
	billID   int64
	build    func(booking queue.Booking) *invoice.Invoice
}

// Build builds the invoice with the booked UUID, ICV, and previous hash, then signs it and computes its hash and QR code. Nothing is
// sent to ZATCA here.
func (d *document) Build(_ context.Context, booking queue.Booking) (queue.SignedInvoice, error) {
	inv := d.build(booking)
	xmlStr, err := inv.ToXML()
	if err != nil {
		return queue.SignedInvoice{}, fmt.Errorf("build XML of %s %d: %w", d.docType, d.sourceID, err)
	}
	signedXML, hash, err := d.svc.SDK.SignInvoice(xmlStr)
	if err != nil {
		return queue.SignedInvoice{}, fmt.Errorf("sign %s %d: %w", d.docType, d.sourceID, err)
	}
	apiReq, err := apiRequest(d.svc, signedXML)
	if err != nil {
		return queue.SignedInvoice{}, err
	}
	if apiReq.UUID != booking.UUID {
		return queue.SignedInvoice{}, fmt.Errorf("signed %s %d carries UUID %s, not the booked %s",
			d.docType, d.sourceID, apiReq.UUID, booking.UUID)
	}
	invoiceHash := apiReq.InvoiceHash
	if invoiceHash == "" {
		invoiceHash = hash
	}
	if invoiceHash == "" {
		return queue.SignedInvoice{}, fmt.Errorf("signed %s %d has no invoice hash", d.docType, d.sourceID)
	}
	qr, err := d.svc.SDK.GenerateQR(signedXML)
	if err != nil {
		return queue.SignedInvoice{}, fmt.Errorf("QR code of %s %d: %w", d.docType, d.sourceID, err)
	}
	kind := queue.InvoiceSimplified
	if inv.IsStandard() {
		kind = queue.InvoiceStandard
	}
	return queue.SignedInvoice{
		DocType:   d.docType,
		SourceID:  d.sourceID,
		BillID:    d.billID,
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
// be a final sale, and carry the envelope's invoice UUID when it has one. The seller's VAT and EGS id come from the branch
// configuration, and the bill needs at least one line with a positive quantity and a price that is not negative.
func verifyBill(cfg *config.Config, msg queue.Message, bill BillRow, products []ProductRow) error {
	switch {
	case bill.BranchID != msg.BranchID:
		return fmt.Errorf("the bill belongs to branch %d, not %d", bill.BranchID, msg.BranchID)
	case bill.State < 1:
		return fmt.Errorf("the bill is not a final sale (state %d)", bill.State)
	case bill.InvoiceUUID.Valid && bill.InvoiceUUID.String != msg.InvoiceUUID:
		return errors.New("the bill carries another invoice UUID")
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

// supplierOf is the seller of an invoice: the branch's company, from its configuration.
func supplierOf(cfg *config.Config) invoice.Party {
	return invoice.Party{
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
	}
}

// buildBillInvoice constructs the invoice (type 388) under a booking. It uses the booked UUID, ICV, and previous hash, and
// never chooses its own.
func buildBillInvoice(cfg *config.Config, b BillRow, products []ProductRow, issueTime time.Time, booking queue.Booking) *invoice.Invoice {
	isStandard := b.BuyerVAT.Valid && b.BuyerVAT.String != ""
	inv := &invoice.Invoice{
		ID:                  fmt.Sprintf("INV-%d", b.ID),
		UUID:                booking.UUID,
		IssueDate:           issueTime,
		IssueTime:           issueTime,
		TypeCode:            invoice.TypeInvoice,
		DocumentCurrency:    "SAR",
		TaxCurrency:         "SAR",
		Supplier:            supplierOf(cfg),
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
