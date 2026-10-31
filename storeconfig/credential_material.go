package storeconfig

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/zatca-go/zatca/credential"
	"github.com/zatca-go/zatca/secutil"
)

// CredentialMaterialStore bridges the canonical CSID lifecycle state to the
// legacy secret-bearing branch_zatca_config row.
type CredentialMaterialStore struct {
	DB     *sql.DB
	EncKey *secutil.Key
}

func NewCredentialMaterialStore(db *sql.DB, encKey *secutil.Key) *CredentialMaterialStore {
	return &CredentialMaterialStore{DB: db, EncKey: encKey}
}

func (s *CredentialMaterialStore) Load(ctx context.Context, identity credential.Identity) (credential.Material, error) {
	if s == nil || s.DB == nil {
		return credential.Material{}, fmt.Errorf("credential material database is not configured")
	}
	if identity.BranchID <= 0 {
		return credential.Material{}, fmt.Errorf("credential material branch_id is required")
	}

	var (
		csr         sql.NullString
		privateKey  sql.NullString
		certificate sql.NullString
		secret      sql.NullString
		requestID   sql.NullString
	)
	err := s.DB.QueryRowContext(ctx, `
		SELECT
			COALESCE(zatca_csr, ''),
			COALESCE(zatca_private_key, ''),
			COALESCE(zatca_production_username, ''),
			COALESCE(zatca_production_password, ''),
			COALESCE(zatca_production_request_id, '')
		FROM branch_zatca_config
		WHERE branch_id = ?
		LIMIT 1
	`, identity.BranchID).Scan(&csr, &privateKey, &certificate, &secret, &requestID)
	if err != nil {
		return credential.Material{}, fmt.Errorf("load credential material for branch %d: %w", identity.BranchID, err)
	}

	privateKeyValue, err := decryptStored(s.EncKey, privateKey.String)
	if err != nil {
		return credential.Material{}, fmt.Errorf("decrypt private key for branch %d: %w", identity.BranchID, err)
	}
	secretValue, err := decryptStored(s.EncKey, secret.String)
	if err != nil {
		return credential.Material{}, fmt.Errorf("decrypt production secret for branch %d: %w", identity.BranchID, err)
	}
	return credential.Material{
		CSR:         csr.String,
		PrivateKey:  privateKeyValue,
		Certificate: certificate.String,
		Secret:      secretValue,
		RequestID:   requestID.String,
	}, nil
}

func (s *CredentialMaterialStore) Persist(ctx context.Context, identity credential.Identity, issued credential.IssuedCredential) error {
	if s == nil || s.DB == nil {
		return fmt.Errorf("credential material database is not configured")
	}
	if identity.BranchID <= 0 {
		return fmt.Errorf("credential material branch_id is required")
	}
	if strings.TrimSpace(issued.Certificate) == "" || strings.TrimSpace(issued.Secret) == "" {
		return fmt.Errorf("rotated credential material is incomplete")
	}
	if s.EncKey == nil {
		return fmt.Errorf("credential encryption key is not configured")
	}

	encryptedSecret, err := s.EncKey.Encrypt(issued.Secret)
	if err != nil {
		return fmt.Errorf("encrypt rotated production secret: %w", err)
	}

	var (
		csrValue        any
		privateKeyValue any
	)
	if strings.TrimSpace(issued.CSR) != "" {
		csrValue = issued.CSR
	}
	if strings.TrimSpace(issued.PrivateKey) != "" {
		privateKeyValue, err = s.EncKey.Encrypt(issued.PrivateKey)
		if err != nil {
			return fmt.Errorf("encrypt rotated private key: %w", err)
		}
	}

	result, err := s.DB.ExecContext(ctx, `
		UPDATE branch_zatca_config
		SET zatca_production_username = ?,
		    zatca_production_password = ?,
		    zatca_production_request_id = ?,
		    zatca_csr = COALESCE(?, zatca_csr),
		    zatca_private_key = COALESCE(?, zatca_private_key),
		    zatca_registered_at = NOW()
		WHERE branch_id = ?
	`, issued.Certificate, encryptedSecret, issued.RequestID, csrValue, privateKeyValue, identity.BranchID)
	if err != nil {
		return fmt.Errorf("persist rotated credential material for branch %d: %w", identity.BranchID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect rotated credential material for branch %d: %w", identity.BranchID, err)
	}
	if affected != 1 {
		return fmt.Errorf("persist rotated credential material for branch %d: row not found", identity.BranchID)
	}
	return nil
}

func decryptStored(key *secutil.Key, value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if key == nil {
		return "", fmt.Errorf("credential encryption key is not configured")
	}
	if !secutil.IsEncrypted(value) {
		return value, nil
	}
	return key.Decrypt(value)
}
