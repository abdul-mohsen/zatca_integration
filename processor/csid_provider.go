package processor

import (
	"context"
	"errors"
	"fmt"

	"github.com/zatca-go/zatca/credential"
	zcrypto "github.com/zatca-go/zatca/crypto"
	"github.com/zatca-go/zatca/zatca"
)

// ServiceCSIDProvider is the production adapter for lifecycle operations.
// Renewal and revocation are deliberately exposed through the narrow
// credential interfaces rather than being called by worker code directly.
type ServiceCSIDProvider struct {
	Service *zatca.Service
}

func (p *ServiceCSIDProvider) Renew(ctx context.Context, request credential.RenewalRequest) (credential.IssuedCredential, error) {
	if err := ctx.Err(); err != nil {
		return credential.IssuedCredential{}, err
	}
	if p == nil || p.Service == nil || p.Service.Client == nil {
		return credential.IssuedCredential{}, errors.New("ZATCA renewal provider is not configured")
	}
	response, err := p.Service.Client.RenewProductionCSID(request.Material.CSR)
	if err != nil {
		return credential.IssuedCredential{}, err
	}
	if response == nil || response.BinarySecurityToken == "" {
		return credential.IssuedCredential{}, fmt.Errorf("renewal returned no production certificate")
	}
	certInfo, err := zcrypto.ParseCertificate(response.BinarySecurityToken)
	if err != nil {
		return credential.IssuedCredential{}, fmt.Errorf("parse renewed production certificate: %w", err)
	}
	if certInfo.SerialNum == nil || certInfo.NotAfter.IsZero() {
		return credential.IssuedCredential{}, fmt.Errorf("renewal certificate has incomplete metadata")
	}
	return credential.IssuedCredential{
		Certificate:          response.BinarySecurityToken,
		Secret:               response.Secret,
		CSR:                  request.Material.CSR,
		PrivateKey:           request.Material.PrivateKey,
		RequestID:            response.RequestID.String(),
		CertificateSerial:    certInfo.SerialNum.String(),
		CertificateExpiresAt: certInfo.NotAfter,
	}, nil
}

func (p *ServiceCSIDProvider) Revoke(ctx context.Context, request credential.RevocationRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p == nil || p.Service == nil || p.Service.Client == nil {
		return errors.New("ZATCA revocation provider is not configured")
	}
	if request.CertificateSerial == "" {
		return fmt.Errorf("certificate serial is required for revocation")
	}
	return p.Service.Client.RevokeProductionCSID(ctx, request.CertificateSerial, request.Reason)
}
