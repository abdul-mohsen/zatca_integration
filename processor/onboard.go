package processor

import (
	"database/sql"
	"errors"
	"fmt"
	"log"

	"github.com/zatca-go/zatca/config"
	"github.com/zatca-go/zatca/secutil"
	"github.com/zatca-go/zatca/zatca"
)

// OnboardBranch performs the full ZATCA onboarding for a branch:
// 1. Load branch + company info from DB
// 2. Build Taxpayer → CSR config
// 3. Generate CSR → Compliance CSID → 6 test invoices → Production CSID
// 4. Save all credentials back to the branch table
//
// The branch_zatca_config row is expected to be pre-populated by the
// backend (the user fills CSR fields + address through the UI). Those
// values take precedence over any company/store joins so what the user
// entered is what gets sent to ZATCA.
func OnboardBranch(db *sql.DB, baseCfg *config.Config, branchID int64, otp string, encKey *secutil.Key) error {
	branch, err := loadOnboardBranch(db, branchID, encKey)
	if err != nil {
		return err
	}

	tp := branch.taxpayer()
	if err := tp.Validate(); err != nil {
		return fmt.Errorf("branch %d: %w", branchID, err)
	}
	// LocationCode ("csr.location.address") must be a 4 alpha + 4 digit
	// short address per ZATCA. The default of all-zeros is a known
	// rejection; require the user to set it explicitly.
	if branch.CSRLocation == "" {
		return fmt.Errorf("branch %d: csr_location is required (4 letters + 4 digits, e.g. RRRD2929)", branchID)
	}

	// Reuse the EGS identity (TIN + UUID) once it has been generated, so
	// retries don't register a different device with ZATCA.
	if branch.CSRTIN != "" {
		tp.TIN = branch.CSRTIN
	}
	if branch.CSRComputerNumber != "" {
		tp.ComputerNumber = branch.CSRComputerNumber
	}

	cfg := tp.Config(baseCfg.Env, otp)
	svc := zatca.New(cfg)

	log.Printf("Onboard branch %d: company=%s branch=%s vat=%s env=%s",
		branchID, branch.CompanyName, branch.BranchName, branch.VATReg, cfg.Env)

	// Step 1: CSR + private key.
	//
	// A CSR is single-shot at ZATCA: once it has been exchanged for a
	// Compliance CSID, submitting the same CSR again returns HTTP 409
	// ("Compliance transaction was submitted/generated before"), regardless
	// of which OTP is attached. So the only safe time to reuse a stored
	// CSR is when onboarding actually completed (i.e. we already saved a
	// compliance certificate). If there's a stored CSR but no compliance
	// certificate, the previous attempt either succeeded without our
	// catching the result, or burned the CSR on ZATCA's side. In both
	// cases the right move on a fresh OTP is to generate a new CSR.
	if branch.ComplianceCert != "" {
		return fmt.Errorf("branch %d: onboarding already completed (compliance certificate is present); revoke and clear branch_zatca_config to re-onboard", branchID)
	}

	var csrPEM, privateKey string
	if branch.ExistingCSR != "" {
		log.Printf("Branch %d: discarding stale CSR (no compliance cert saved); generating fresh CSR with new OTP", branchID)
	}
	csrResult, err := svc.SDK.GenerateCSR(cfg.CSR)
	if err != nil {
		return fmt.Errorf("step 1 (generate CSR): %w", err)
	}
	csrPEM = csrResult.CSR
	privateKey = csrResult.PrivateKey
	// Persist CSR + key immediately so a failure later can be debugged
	// without re-running CSR generation.
	encKey2, err := encKey.Encrypt(privateKey)
	if err != nil {
		return fmt.Errorf("step 1 (encrypt private key): %w", err)
	}
	if err := saveOnboardCSR(db, branchID, cfg, csrPEM, encKey2, tp.TIN, tp.ComputerNumber); err != nil {
		return fmt.Errorf("step 1 (persist CSR): %w", err)
	}
	svc.SDK.SetCredentials(privateKey, "")

	// Run the rest of onboarding (compliance CSID → 6 invoices → production CSID)
	// using the already-generated key.
	result, err := svc.OnboardWithCSR(csrPEM, privateKey)
	if err != nil {
		return fmt.Errorf("onboarding failed: %w", err)
	}

	log.Printf("Branch %d: Onboarding succeeded! Saving credentials...", branchID)

	encPriv, err := encKey.Encrypt(privateKey)
	if err != nil {
		return fmt.Errorf("encrypt private key: %w", err)
	}
	encCompSecret, err := encKey.Encrypt(result.ComplianceSecret)
	if err != nil {
		return fmt.Errorf("encrypt compliance secret: %w", err)
	}
	encProdSecret, err := encKey.Encrypt(result.ProductionSecret)
	if err != nil {
		return fmt.Errorf("encrypt production secret: %w", err)
	}

	// NOTE: `zatca_otp` is intentionally NOT written here. The OTP arrives
	// via NATS (single-use) and persisting it would only retain plaintext
	// PII. Some deployments' `branch_zatca_config` tables also lack the
	// column entirely (Error 1054: Unknown column 'zatca_otp'), so writing
	// it would break onboarding on those tenants. The post-insert
	// "clear OTP" UPDATE below is best-effort for legacy schemas that DO
	// have the column.
	credQ := `INSERT INTO branch_zatca_config
		(branch_id, csr_org_identifier, csr_org_unit, csr_org_name, csr_country, csr_location, business_category, seller_vat, seller_crn, street, building, district, postal_code, zatca_csr, zatca_private_key, zatca_compliance_certificate, zatca_compliance_secret, zatca_compliance_request_id, zatca_production_username, zatca_production_password, zatca_production_request_id, zatca_registered_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NOW())
	ON DUPLICATE KEY UPDATE
		csr_org_identifier = VALUES(csr_org_identifier),
		csr_org_unit = VALUES(csr_org_unit),
		csr_org_name = VALUES(csr_org_name),
		csr_country = VALUES(csr_country),
		csr_location = VALUES(csr_location),
		business_category = VALUES(business_category),
		seller_vat = VALUES(seller_vat),
		seller_crn = VALUES(seller_crn),
		street = VALUES(street),
		building = VALUES(building),
		district = VALUES(district),
		postal_code = VALUES(postal_code),
		zatca_csr = VALUES(zatca_csr),
		zatca_private_key = VALUES(zatca_private_key),
		zatca_compliance_certificate = VALUES(zatca_compliance_certificate),
		zatca_compliance_secret = VALUES(zatca_compliance_secret),
		zatca_compliance_request_id = VALUES(zatca_compliance_request_id),
		zatca_production_username = VALUES(zatca_production_username),
		zatca_production_password = VALUES(zatca_production_password),
		zatca_production_request_id = VALUES(zatca_production_request_id),
		zatca_registered_at = NOW()`

	if _, err := db.Exec(credQ,
		branchID,
		cfg.CSR.OrgIdentifier,
		cfg.CSR.OrgUnit,
		cfg.CSR.OrgName,
		cfg.CSR.Country,
		cfg.CSR.Location,
		cfg.CSR.BusinessCategory,
		branch.VATReg,
		branch.CRN,
		branch.Street,
		branch.Building,
		branch.District,
		branch.PostalCode,
		csrPEM,
		encPriv,
		result.ComplianceCert,
		encCompSecret,
		result.ComplianceRequestID,
		result.ProductionCert,
		encProdSecret,
		result.ProductionRequestID,
	); err != nil {
		return fmt.Errorf("save credentials to DB: %w", err)
	}

	log.Printf("Branch %d: Registration complete.", branchID)

	// Clear the OTP — it's single-use and should not be retained in plaintext.
	if _, err := db.Exec(`UPDATE branch_zatca_config SET zatca_otp = '' WHERE branch_id = ?`, branchID); err != nil {
		log.Printf("WARN: branch %d: clear OTP: %v", branchID, err)
	}
	return nil
}

// onboardBranchData holds the data loaded from the DB for onboarding.
type onboardBranchData struct {
	CompanyName   string
	CompanyNameAR string
	VATReg        string
	CRN           string
	BranchName    string
	Street        string
	Building      string
	District      string
	City          string
	PostalCode    string
	Country       string
	BusinessCat   string
	CSRLocation   string

	// Stable EGS identity, persisted across retries.
	CSRTIN            string
	CSRComputerNumber string

	// Existing CSR / private key (if a previous onboard attempt already
	// generated them). Empty when this is a fresh onboard.
	ExistingCSR        string
	ExistingPrivateKey string

	// ComplianceCert is non-empty once onboarding has fully succeeded.
	// Used to refuse a redundant onboard attempt instead of burning a
	// new CSR or hitting ZATCA's 409 dedup.
	ComplianceCert string
}

func (b *onboardBranchData) taxpayer() *config.Taxpayer {
	return &config.Taxpayer{
		CompanyName:      b.CompanyName,
		CompanyNameAR:    b.CompanyNameAR,
		VATNumber:        b.VATReg,
		CRN:              b.CRN,
		BranchName:       b.BranchName,
		BusinessCategory: b.BusinessCat,
		Street:           b.Street,
		Building:         b.Building,
		District:         b.District,
		City:             b.City,
		PostalCode:       b.PostalCode,
		Country:          b.Country,
		LocationCode:     b.CSRLocation,
	}
}

// loadOnboardBranch reads everything we need to onboard a branch.
// branch_zatca_config is the authoritative source for the CSR-facing
// fields (the user filled them through the backend UI). The company/store
// join is used purely as a fallback so this still works for branches
// where the row hasn't been pre-populated.
func loadOnboardBranch(db *sql.DB, branchID int64, encKey *secutil.Key) (*onboardBranchData, error) {
	q := `SELECT
		COALESCE(c.name, '') AS company_name,
		COALESCE(c.name_ar, '') AS company_name_ar,
		COALESCE(c.vat_registration_number, '') AS company_vat,
		COALESCE(c.commercial_registration_number, '') AS company_crn,
		COALESCE(b.name, '') AS branch_name,
		COALESCE(s.street_name, '') AS s_street,
		COALESCE(s.building_number, '') AS s_building,
		COALESCE(s.district, '') AS s_district,
		COALESCE(s.city, '') AS s_city,
		COALESCE(s.postal_code, '') AS s_postal,
		COALESCE(s.country, 'SA') AS s_country,
		COALESCE(c.business_category, 'Supply activities') AS company_biz_cat,
		COALESCE(bz.csr_org_identifier, '') AS bz_org_id,
		COALESCE(bz.csr_org_unit, '') AS bz_org_unit,
		COALESCE(bz.csr_org_name, '') AS bz_org_name,
		COALESCE(bz.csr_country, '') AS bz_country,
		COALESCE(bz.csr_location, '') AS bz_location,
		COALESCE(bz.business_category, '') AS bz_biz_cat,
		COALESCE(bz.seller_vat, '') AS bz_vat,
		COALESCE(bz.seller_crn, '') AS bz_crn,
		COALESCE(bz.street, '') AS bz_street,
		COALESCE(bz.building, '') AS bz_building,
		COALESCE(bz.district, '') AS bz_district,
		COALESCE(bz.postal_code, '') AS bz_postal,
		COALESCE(bz.zatca_csr, '') AS bz_csr,
		COALESCE(bz.zatca_private_key, '') AS bz_priv,
		COALESCE(bz.csr_tin, '') AS bz_tin,
		COALESCE(bz.csr_computer_number, '') AS bz_uuid,
		COALESCE(bz.zatca_compliance_certificate, '') AS bz_comp_cert
	FROM branches b
	JOIN company c ON c.id = b.company_id
	LEFT JOIN store s ON s.id = (
		SELECT s2.id FROM store s2 WHERE s2.branch_id = b.id ORDER BY s2.id LIMIT 1
	)
	LEFT JOIN branch_zatca_config bz ON bz.branch_id = b.id
	WHERE b.id = ?`

	var (
		companyName, companyNameAR, companyVAT, companyCRN, branchName      string
		sStreet, sBuilding, sDistrict, sCity, sPostal, sCountry, companyBiz string
		bzOrgID, bzOrgUnit, bzOrgName, bzCountry, bzLocation, bzBiz         string
		bzVAT, bzCRN, bzStreet, bzBuilding, bzDistrict, bzPostal            string
		bzCSR, bzPriv, bzTIN, bzUUID, bzCompCert                            string
	)
	err := db.QueryRow(q, branchID).Scan(
		&companyName, &companyNameAR, &companyVAT, &companyCRN, &branchName,
		&sStreet, &sBuilding, &sDistrict, &sCity, &sPostal, &sCountry, &companyBiz,
		&bzOrgID, &bzOrgUnit, &bzOrgName, &bzCountry, &bzLocation, &bzBiz,
		&bzVAT, &bzCRN, &bzStreet, &bzBuilding, &bzDistrict, &bzPostal,
		&bzCSR, &bzPriv, &bzTIN, &bzUUID, &bzCompCert,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("branch %d not found", branchID)
		}
		return nil, fmt.Errorf("load branch %d: %w", branchID, err)
	}

	pick := func(primary, fallback string) string {
		if primary != "" {
			return primary
		}
		return fallback
	}

	d := &onboardBranchData{
		CompanyName:       pick(bzOrgName, companyName),
		CompanyNameAR:     companyNameAR,
		VATReg:            pick(bzVAT, companyVAT),
		CRN:               pick(bzCRN, companyCRN),
		BranchName:        pick(bzOrgUnit, branchName),
		BusinessCat:       pick(bzBiz, companyBiz),
		Street:            pick(bzStreet, sStreet),
		Building:          pick(bzBuilding, sBuilding),
		District:          pick(bzDistrict, sDistrict),
		City:              sCity,
		PostalCode:        pick(bzPostal, sPostal),
		Country:           pick(bzCountry, sCountry),
		CSRLocation:       bzLocation,
		CSRTIN:            bzTIN,
		CSRComputerNumber: bzUUID,
		ExistingCSR:       bzCSR,
		ComplianceCert:    bzCompCert,
	}

	if bzPriv != "" {
		dec, derr := encKey.Decrypt(bzPriv)
		if derr != nil {
			return nil, fmt.Errorf("decrypt existing private key: %w", derr)
		}
		d.ExistingPrivateKey = dec
	}

	return d, nil
}

// saveOnboardCSR persists the CSR + encrypted private key after step 1
// so that retries can reuse them and operators can debug failures.
func saveOnboardCSR(db *sql.DB, branchID int64, cfg *config.Config, csrPEM, encryptedPrivateKey, tin, computerNumber string) error {
	q := `INSERT INTO branch_zatca_config
		(branch_id, csr_org_identifier, csr_org_unit, csr_org_name, csr_country, csr_location, business_category, zatca_csr, zatca_private_key, csr_tin, csr_computer_number)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON DUPLICATE KEY UPDATE
		csr_org_identifier = VALUES(csr_org_identifier),
		csr_org_unit = VALUES(csr_org_unit),
		csr_org_name = VALUES(csr_org_name),
		csr_country = VALUES(csr_country),
		csr_location = VALUES(csr_location),
		business_category = VALUES(business_category),
		zatca_csr = VALUES(zatca_csr),
		zatca_private_key = VALUES(zatca_private_key),
		csr_tin = VALUES(csr_tin),
		csr_computer_number = VALUES(csr_computer_number)`
	_, err := db.Exec(q, branchID,
		cfg.CSR.OrgIdentifier, cfg.CSR.OrgUnit, cfg.CSR.OrgName, cfg.CSR.Country,
		cfg.CSR.Location, cfg.CSR.BusinessCategory,
		csrPEM, encryptedPrivateKey, tin, computerNumber,
	)
	return err
}
