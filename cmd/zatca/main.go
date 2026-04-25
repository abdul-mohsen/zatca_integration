package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/zatca-go/zatca/config"
	"github.com/zatca-go/zatca/invoice"
	"github.com/zatca-go/zatca/zatca"
)

func main() {
	zatcaEnv := flag.String("zatca-env", "sandbox", "ZATCA environment: sandbox, simulation, production")
	otp := flag.String("otp", "123456", "OTP from ZATCA Fatoora portal")
	flag.Parse()

	args := flag.Args()
	if len(args) == 0 {
		printUsage()
		os.Exit(1)
	}

	tp := &config.Taxpayer{
		CompanyName:      "Test Company Ltd",
		CompanyNameAR:    "\u0634\u0631\u0643\u0629 \u062a\u062c\u0631\u0628\u0629",
		VATNumber:        "399999999900003",
		CRN:              "4030000000",
		BranchName:       "Main Branch",
		BusinessCategory: "Supply activities",
		Street:           "King Fahad Road",
		Building:         "1234",
		District:         "Al-Olaya",
		City:             "Riyadh",
		PostalCode:       "12345",
	}
	cfg := tp.Config(config.Environment(*zatcaEnv), *otp)
	svc := zatca.New(cfg)

	switch args[0] {
	case "onboard":
		cmdOnboard(svc)
	case "report":
		cmdReport(svc)
	case "clear":
		cmdClear(svc)
	case "csr":
		cmdCSR(svc)
	case "xml":
		cmdXML(svc)
	case "validate":
		cmdValidate(svc, args[1:])
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", args[0])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `ZATCA E-Invoicing CLI

Usage: zatca [-zatca-env sandbox] [-otp 123456] <command>

Commands:
  onboard   Run the full onboarding flow (CSR → Compliance → Production)
  csr       Generate a CSR via SDK
  xml       Generate a sample invoice XML
  validate  Validate an invoice XML via SDK
  report    Build, sign, and report a sample simplified invoice (B2C)
  clear     Build, sign, and clear a sample standard invoice (B2B)

Options:
  -zatca-env string   ZATCA environment (default "sandbox")
  -otp string         OTP from ZATCA portal (default "123456")
`)
}

func cmdOnboard(svc *zatca.Service) {
	fmt.Println("=== ZATCA Onboarding ===")
	fmt.Println("Step 1: Generating CSR...")
	result, err := svc.Onboard()
	if err != nil {
		log.Fatalf("Onboarding failed: %v", err)
	}

	fmt.Println("\n=== Onboarding Complete ===")
	fmt.Printf("Compliance Request ID: %s\n", result.ComplianceRequestID)
	fmt.Printf("Production Request ID: %s\n", result.ProductionRequestID)
	fmt.Printf("Production Cert: %s...\n", truncate(result.ProductionCert, 40))
	fmt.Printf("Production Secret: %s...\n", truncate(result.ProductionSecret, 20))

	// Save credentials to file
	creds := map[string]string{
		"production_cert":       result.ProductionCert,
		"production_secret":     result.ProductionSecret,
		"production_request_id": result.ProductionRequestID,
		"private_key":           result.PrivateKey,
	}
	data, _ := json.MarshalIndent(creds, "", "  ")
	if err := os.WriteFile("output/credentials.json", data, 0600); err != nil {
		fmt.Printf("Warning: could not save credentials: %v\n", err)
	} else {
		fmt.Println("\nCredentials saved to output/credentials.json")
	}
}

func cmdCSR(svc *zatca.Service) {
	fmt.Println("=== Generate CSR ===")
	result, err := svc.SDK.GenerateCSR(svc.Config.CSR)
	if err != nil {
		log.Fatalf("CSR generation failed: %v", err)
	}
	fmt.Println("CSR:")
	fmt.Println(result.CSR)
	fmt.Println("\nPrivate Key:")
	fmt.Println(result.PrivateKey)
}

func cmdXML(svc *zatca.Service) {
	inv := buildSampleInvoice(svc, invoice.SubTypeStandard)
	xmlStr, err := inv.ToXML()
	if err != nil {
		log.Fatalf("XML generation failed: %v", err)
	}
	fmt.Println(xmlStr)
}

func cmdValidate(svc *zatca.Service, args []string) {
	if len(args) == 0 {
		log.Fatal("Usage: zatca validate <invoice.xml>")
	}
	data, err := os.ReadFile(args[0])
	if err != nil {
		log.Fatalf("Read file: %v", err)
	}
	result, err := svc.SDK.ValidateInvoice(string(data))
	if err != nil {
		log.Fatalf("Validation failed: %v", err)
	}
	fmt.Println(result)
}

func cmdReport(svc *zatca.Service) {
	fmt.Println("=== Report Simplified Invoice (B2C) ===")
	inv := buildSampleInvoice(svc, invoice.SubTypeSimplified)
	result, err := svc.ReportInvoice(inv)
	if err != nil {
		log.Fatalf("Report failed: %v", err)
	}
	fmt.Printf("Status: %s\n", result.Status)
	fmt.Printf("Hash: %s\n", result.InvoiceHash)
	if len(result.Warnings) > 0 {
		fmt.Printf("Warnings: %v\n", result.Warnings)
	}
}

func cmdClear(svc *zatca.Service) {
	fmt.Println("=== Clear Standard Invoice (B2B) ===")
	inv := buildSampleInvoice(svc, invoice.SubTypeStandard)
	result, err := svc.ClearInvoice(inv)
	if err != nil {
		log.Fatalf("Clearance failed: %v", err)
	}
	fmt.Printf("Status: %s\n", result.Status)
	fmt.Printf("Hash: %s\n", result.InvoiceHash)
	if result.ClearedXML != "" {
		fmt.Printf("Cleared XML: %d bytes\n", len(result.ClearedXML))
	}
	if len(result.Warnings) > 0 {
		fmt.Printf("Warnings: %v\n", result.Warnings)
	}
}

func buildSampleInvoice(svc *zatca.Service, subType invoice.SubType) *invoice.Invoice {
	now := time.Now()
	return &invoice.Invoice{
		ID:        fmt.Sprintf("INV-%s", now.Format("20060102150405")),
		UUID:      "550e8400-e29b-41d4-a716-446655440000",
		IssueDate: now,
		IssueTime: now,
		TypeCode:  invoice.TypeInvoice,
		SubType:   subType,
		Supplier: invoice.Party{
			RegistrationName:   svc.Config.Seller.Name,
			RegistrationNameAR: svc.Config.Seller.NameAR,
			VAT:                svc.Config.Seller.VAT,
			SchemeID:           "CRN",
			ID:                 svc.Config.Seller.CRN,
			Street:             svc.Config.Seller.Street,
			Building:           svc.Config.Seller.Building,
			District:           svc.Config.Seller.District,
			City:               svc.Config.Seller.City,
			PostalCode:         svc.Config.Seller.PostalCode,
			Country:            svc.Config.Seller.Country,
		},
		Customer: invoice.Party{
			RegistrationName: "Fatoora Samples LTD",
			VAT:              "399999999800003",
			Street:           "Prince Sultan",
			Building:         "2322",
			District:         "Al-Murabba",
			City:             "Riyadh",
			PostalCode:       "23333",
			Country:          "SA",
		},
		DeliveryDate:        now,
		PaymentMeans:        invoice.PaymentCash,
		PreviousInvoiceHash: "NWZlY2ViNjZmZmM4NmYzOGQ5NTI3ODZjNmQ2OTZjNzljMmRiYzIzOWRkNGU5MWI0NjcyOWQ3M2EyN2ZiNTdlOQ==",
		InvoiceCounterValue: 1,
		Lines: []invoice.LineItem{
			{
				Name:        "Laptop Computer",
				Quantity:    2,
				UnitCode:    "PCE",
				UnitPrice:   3000.00,
				TaxCategory: invoice.TaxStandard,
				TaxPercent:  15.00,
			},
			{
				Name:        "Wireless Mouse",
				Quantity:    5,
				UnitCode:    "PCE",
				UnitPrice:   50.00,
				TaxCategory: invoice.TaxStandard,
				TaxPercent:  15.00,
			},
		},
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
