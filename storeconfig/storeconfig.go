package storeconfig

import (
	"database/sql"
	"encoding/base64"
	"fmt"
	"net/http"
	"time"

	"github.com/zatca-go/zatca/config"
	"github.com/zatca-go/zatca/secutil"
	"github.com/zatca-go/zatca/zatca"
)

// BranchZATCA holds ZATCA credentials and seller info for a specific branch.
type BranchZATCA struct {
	BranchID           int64
	StoreID            int64
	CompanyName        string
	CompanyNameAR      string
	VATReg             string
	CRN                string
	BranchName         string
	Street             string
	Building           string
	District           string
	City               string
	PostalCode         string
	Country            string
	ProductionUsername string
	ProductionPassword string
	PrivateKey         string // PEM-encoded EC private key
	Certificate        string // Base64-encoded certificate from ZATCA
}

// LoadBranchZATCA loads ZATCA credentials for a given branch from the database.
// Queries branch → store → company → branch_zatca to build seller info and ZATCA credentials.
func LoadBranchZATCA(db *sql.DB, branchID int64, encKey *secutil.Key) (*BranchZATCA, error) {
	q := `SELECT
		b.id,
		COALESCE(s.id, 0) as store_id,
		COALESCE(c.name, '') as company_name,
		COALESCE(c.name_ar, '') as company_name_ar,
		COALESCE(c.vat_registration_number, '') as vat_reg,
		COALESCE(c.commercial_registration_number, '') as crn,
		COALESCE(b.name, '') as branch_name,
		COALESCE(s.street_name, '') as street,
		COALESCE(s.building_number, '') as building,
		COALESCE(s.district, '') as district,
		COALESCE(s.city, '') as city,
		COALESCE(s.postal_code, '') as postal_code,
		COALESCE(s.country, 'SA') as country,
		COALESCE(bz.zatca_production_username, '') as zatca_username,
		COALESCE(bz.zatca_production_password, '') as zatca_password,
		COALESCE(bz.zatca_private_key, '') as zatca_private_key
	FROM branches b
	JOIN company c ON c.id = b.company_id
	LEFT JOIN store s ON s.id = (
		SELECT s2.id FROM store s2 WHERE s2.branch_id = b.id ORDER BY s2.id LIMIT 1
	)
	LEFT JOIN branch_zatca_config bz ON bz.branch_id = b.id
	WHERE b.id = ?`

	var bz BranchZATCA
	err := db.QueryRow(q, branchID).Scan(
		&bz.BranchID,
		&bz.StoreID,
		&bz.CompanyName,
		&bz.CompanyNameAR,
		&bz.VATReg,
		&bz.CRN,
		&bz.BranchName,
		&bz.Street,
		&bz.Building,
		&bz.District,
		&bz.City,
		&bz.PostalCode,
		&bz.Country,
		&bz.ProductionUsername,
		&bz.ProductionPassword,
		&bz.PrivateKey,
	)
	if err != nil {
		return nil, fmt.Errorf("load ZATCA config for branch %d: %w", branchID, err)
	}

	// Decrypt sensitive fields
	if bz.PrivateKey, err = encKey.Decrypt(bz.PrivateKey); err != nil {
		return nil, fmt.Errorf("decrypt private_key for branch %d: %w", branchID, err)
	}
	if bz.ProductionPassword, err = encKey.Decrypt(bz.ProductionPassword); err != nil {
		return nil, fmt.Errorf("decrypt production_password for branch %d: %w", branchID, err)
	}

	// The production username IS the base64-encoded certificate (BinarySecurityToken)
	bz.Certificate = bz.ProductionUsername
	return &bz, nil
}

// IsRegistered returns true if the branch has production credentials.
func (bz *BranchZATCA) IsRegistered() bool {
	return bz.ProductionUsername != "" && bz.ProductionPassword != ""
}

// ToConfig builds a config.Config from branch data, merging with a base config
// (which provides environment, OTP, and CSR defaults).
func (bz *BranchZATCA) ToConfig(base *config.Config) *config.Config {
	return &config.Config{
		Env:                base.Env,
		OTP:                base.OTP,
		XMLDir:             base.XMLDir,
		TenantID:           base.TenantID,
		ProductionUsername: bz.ProductionUsername,
		ProductionPassword: bz.ProductionPassword,
		CSR: config.CSRConfig{
			CommonName:       base.CSR.CommonName,
			SerialNumber:     base.CSR.SerialNumber,
			OrgIdentifier:    bz.VATReg,
			OrgUnit:          bz.BranchName,
			OrgName:          bz.CompanyName,
			Country:          bz.Country,
			InvoiceType:      base.CSR.InvoiceType,
			Location:         base.CSR.Location,
			BusinessCategory: base.CSR.BusinessCategory,
		},
		Seller: config.SellerInfo{
			Name:       bz.CompanyName,
			NameAR:     bz.CompanyNameAR,
			VAT:        bz.VATReg,
			CRN:        bz.CRN,
			Street:     bz.Street,
			Building:   bz.Building,
			District:   bz.District,
			City:       bz.City,
			PostalCode: bz.PostalCode,
			Country:    bz.Country,
		},
	}
}

// NewService creates a zatca.Service configured for this specific branch.
// It injects the branch's private key and certificate into the SDK so the Docker
// container can sign invoices for this branch.
func (bz *BranchZATCA) NewService(base *config.Config) *zatca.Service {
	cfg := bz.ToConfig(base)
	svc := zatca.New(cfg)
	if bz.PrivateKey != "" && bz.Certificate != "" {
		svc.SDK.SetCredentials(bz.PrivateKey, bz.CertificatePEM())
	}
	return svc
}

// CertificatePEM returns the certificate body suitable for the SDK's cert.pem.
//
// ZATCA's /production/csids endpoint returns `binarySecurityToken` =
// Base64(<cert.pem body>), where the inner body is itself raw Base64(DER)
// (same shape as the SDK's shipped Data/Certificates/cert.pem). The Java SDK's
// InvoiceSigningService reads cert.pem and Base64-decodes it ONCE, then calls
// CertificateFactory.generateCertificate(...). So cert.pem must contain the
// inner Base64(DER), NOT the raw doubly-encoded token. Writing the raw token
// fails with "[ERROR] InvoiceSigningService - failed to sign invoice
// [please provide a valid certificate]" (reproduced in Docker).
//
// Therefore: base64-decode the stored token once and return the decoded body.
// If the decode fails (legacy data already stored decoded), fall back to the
// stored value unchanged.
func (bz *BranchZATCA) CertificatePEM() string {
	if bz.Certificate == "" {
		return ""
	}
	if decoded, err := base64.StdEncoding.DecodeString(bz.Certificate); err == nil && len(decoded) > 0 {
		return string(decoded)
	}
	return bz.Certificate
}

// ZATCAStatus represents the connection state of a branch with ZATCA.
type ZATCAStatus string

const (
	StatusNotRegistered ZATCAStatus = "not_registered" // No credentials stored
	StatusRegistered    ZATCAStatus = "registered"     // Has credentials, not verified
	StatusConnected     ZATCAStatus = "connected"      // Credentials work, API reachable
	StatusError         ZATCAStatus = "error"          // Credentials exist but API call failed
)

// BranchStatus holds the ZATCA connection state for a single branch.
type BranchStatus struct {
	BranchID   int64       `json:"branch_id"`
	StoreID    int64       `json:"store_id"`
	BranchName string      `json:"branch_name"`
	Company    string      `json:"company"`
	VAT        string      `json:"vat"`
	Status     ZATCAStatus `json:"status"`
	Message    string      `json:"message,omitempty"`
}

// LoadAllBranchStatuses returns the ZATCA registration status for all active branches.
func LoadAllBranchStatuses(db *sql.DB) ([]BranchStatus, error) {
	q := `SELECT
		b.id,
		COALESCE(s.id, 0) as store_id,
		COALESCE(b.name, '') as branch_name,
		COALESCE(c.name, '') as company_name,
		COALESCE(c.vat_registration_number, '') as vat_reg,
		COALESCE(bz.zatca_production_username, '') as zatca_username,
		COALESCE(bz.zatca_production_password, '') as zatca_password
	FROM branches b
	JOIN company c ON c.id = b.company_id
	LEFT JOIN store s ON s.id = (
		SELECT s2.id FROM store s2 WHERE s2.branch_id = b.id ORDER BY s2.id LIMIT 1
	)
	LEFT JOIN branch_zatca_config bz ON bz.branch_id = b.id
	WHERE b.is_active = 1
	ORDER BY b.id`

	rows, err := db.Query(q)
	if err != nil {
		return nil, fmt.Errorf("query branch statuses: %w", err)
	}
	defer rows.Close()

	var statuses []BranchStatus
	for rows.Next() {
		var (
			branchID int64
			storeID  int64
			branch   string
			company  string
			vat      string
			username string
			password string
		)
		if err := rows.Scan(&branchID, &storeID, &branch, &company, &vat, &username, &password); err != nil {
			return nil, fmt.Errorf("scan branch status: %w", err)
		}

		st := BranchStatus{
			BranchID:   branchID,
			StoreID:    storeID,
			BranchName: branch,
			Company:    company,
			VAT:        vat,
		}
		if username == "" || password == "" {
			st.Status = StatusNotRegistered
			st.Message = "No ZATCA credentials configured"
		} else {
			st.Status = StatusRegistered
			st.Message = "Credentials stored"
		}
		statuses = append(statuses, st)
	}
	return statuses, rows.Err()
}

// CheckZATCAConnection verifies that a branch's credentials are accepted by ZATCA
// by making a lightweight production API ping. Updates the BranchStatus in place.
func CheckZATCAConnection(st *BranchStatus, baseCfg *config.Config, db *sql.DB, encKey *secutil.Key) {
	if st.Status == StatusNotRegistered {
		return
	}

	bz, err := LoadBranchZATCA(db, st.BranchID, encKey)
	if err != nil {
		st.Status = StatusError
		st.Message = fmt.Sprintf("Failed to load branch config: %v", err)
		return
	}

	storeCfg := bz.ToConfig(baseCfg)
	url := storeCfg.Env.BaseURL()

	// Simple HTTP GET to the base URL to check reachability + credential validity
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		st.Status = StatusError
		st.Message = fmt.Sprintf("Failed to create request: %v", err)
		return
	}

	resp, err := client.Do(req)
	if err != nil {
		st.Status = StatusError
		st.Message = fmt.Sprintf("ZATCA API unreachable: %v", err)
		return
	}
	resp.Body.Close()

	st.Status = StatusConnected
	st.Message = "ZATCA API reachable"
}
