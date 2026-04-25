package sdk

import (
	"bytes"
	"fmt"
	"log"
	"os/exec"
	"strings"

	"github.com/zatca-go/zatca/config"
)

// SDK wraps the ZATCA CLI tool (fatoora) running as a local subprocess.
type SDK struct {
	env        config.Environment
	privateKey string // PEM-encoded private key (used for signing)
	certPEM    string // PEM-encoded certificate (used for signing)
}

// New creates a new SDK wrapper.
func New(cfg *config.Config) *SDK {
	return &SDK{
		env: cfg.Env,
	}
}

// SetCredentials sets the private key and certificate for signing operations.
// These are written to the SDK's certificate paths before each sign/QR/request command.
func (s *SDK) SetCredentials(privateKeyPEM, certPEM string) {
	s.privateKey = privateKeyPEM
	s.certPEM = certPEM
}

// credentialScript returns a bash snippet that writes the private key and certificate
// to the paths expected by the ZATCA SDK.
func (s *SDK) credentialScript() string {
	if s.privateKey == "" && s.certPEM == "" {
		return ""
	}
	var sb strings.Builder
	if s.privateKey != "" {
		sb.WriteString(fmt.Sprintf("cat > /SDK/zatca-einvoicing-sdk-238-R4.0.0/Data/Certificates/ec-secp256k1-priv-key.pem << 'KEYEOF'\n%s\nKEYEOF\n", s.privateKey))
	}
	if s.certPEM != "" {
		sb.WriteString(fmt.Sprintf("cat > /SDK/zatca-einvoicing-sdk-238-R4.0.0/Data/Certificates/cert.pem << 'CERTEOF'\n%s\nCERTEOF\n", s.certPEM))
	}
	return sb.String()
}

// CSRResult holds the output from CSR generation.
type CSRResult struct {
	CSR        string // PEM-encoded CSR
	PrivateKey string // PEM-encoded EC private key
}

// GenerateCSR generates a Certificate Signing Request using the SDK.
func (s *SDK) GenerateCSR(csrCfg config.CSRConfig) (*CSRResult, error) {
	// Write CSR config properties
	props := fmt.Sprintf(`csr.common.name=%s
csr.serial.number=%s
csr.organization.identifier=%s
csr.organization.unit.name=%s
csr.organization.name=%s
csr.country.name=%s
csr.invoice.type=%s
csr.location.address=%s
csr.industry.business.category=%s`,
		csrCfg.CommonName,
		csrCfg.SerialNumber,
		csrCfg.OrgIdentifier,
		csrCfg.OrgUnit,
		csrCfg.OrgName,
		csrCfg.Country,
		csrCfg.InvoiceType,
		csrCfg.Location,
		csrCfg.BusinessCategory,
	)

	envFlag := s.envFlag()
	log.Printf("SDK.GenerateCSR: envFlag=%q CommonName=%s OrgUnit=%s", envFlag, csrCfg.CommonName, csrCfg.OrgUnit)

	// Build the docker command that:
	// 1. Writes CSR config to a temp file
	// 2. Runs fatoora -csr with -pem
	// 3. Reads the generated files
	script := fmt.Sprintf(`
echo '%s' > /tmp/csr.properties
cd /SDK/zatca-einvoicing-sdk-238-R4.0.0/Apps
fatoora -csr -csrConfig /tmp/csr.properties -pem %s 1>&2
CSR_FILE=$(ls -t generated-csr-*.csr 2>/dev/null | head -1)
KEY_FILE=$(ls -t generated-private-key-*.key 2>/dev/null | head -1)
if [ -z "$CSR_FILE" ] || [ -z "$KEY_FILE" ]; then
  echo "ERROR: CSR generation failed" >&2
  exit 1
fi
echo "===CSR==="
cat "$CSR_FILE"
echo "===KEY==="
cat "$KEY_FILE"
`, strings.ReplaceAll(props, "'", "'\\''"), envFlag)

	stdout, stderr, err := s.localExec(script)
	if err != nil {
		return nil, fmt.Errorf("GenerateCSR: %w\nstderr: %s", err, stderr)
	}

	csr, key, err := parseCSROutput(stdout)
	if err != nil {
		return nil, fmt.Errorf("GenerateCSR: parsing output: %w\nraw output: %s", err, stdout)
	}

	return &CSRResult{CSR: csr, PrivateKey: key}, nil
}

// ValidateInvoice validates an invoice XML using the SDK.
func (s *SDK) ValidateInvoice(xmlContent string) (string, error) {
	script := fmt.Sprintf(`
cat > /tmp/invoice.xml << 'XMLEOF'
%s
XMLEOF
fatoora -validate -invoice /tmp/invoice.xml %s
`, xmlContent, s.envFlag())

	stdout, stderr, err := s.localExec(script)
	combined := stdout + "\n" + stderr
	if err != nil {
		// SDK returns exit code 1 even for validation failures (which is valid output)
		if strings.Contains(combined, "GLOBAL VALIDATION RESULT") {
			return combined, nil
		}
		return combined, fmt.Errorf("ValidateInvoice: %w", err)
	}
	return combined, nil
}

// GenerateHash generates the hash for an invoice XML.
func (s *SDK) GenerateHash(xmlContent string) (string, error) {
	script := fmt.Sprintf(`
cat > /tmp/invoice.xml << 'XMLEOF'
%s
XMLEOF
fatoora -generateHash -invoice /tmp/invoice.xml %s
`, xmlContent, s.envFlag())

	stdout, stderr, err := s.localExec(script)
	combined := stdout + "\n" + stderr
	if err != nil {
		return "", fmt.Errorf("GenerateHash: %w\noutput: %s", err, combined)
	}

	// Parse hash from output: "*** INVOICE HASH = <hash>"
	for _, line := range strings.Split(combined, "\n") {
		if strings.Contains(line, "INVOICE HASH =") {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				return strings.TrimSpace(parts[1]), nil
			}
		}
	}
	return "", fmt.Errorf("GenerateHash: could not find hash in output: %s", combined)
}

// SignInvoice signs an invoice XML using the SDK.
func (s *SDK) SignInvoice(xmlContent string) (signedXML string, hash string, err error) {
	envFlag := s.envFlag()
	log.Printf("SDK.SignInvoice: envFlag=%q hasKey=%t hasCert=%t xmlLen=%d", envFlag, s.privateKey != "", s.certPEM != "", len(xmlContent))

	script := fmt.Sprintf(`%s
cat > /tmp/invoice.xml << 'XMLEOF'
%s
XMLEOF
fatoora -sign -invoice /tmp/invoice.xml -signedInvoice /tmp/signed.xml %s
echo "===SIGNED==="
cat /tmp/signed.xml
`, s.credentialScript(), xmlContent, envFlag)

	stdout, stderr, errExec := s.localExec(script)
	if errExec != nil {
		return "", "", fmt.Errorf("SignInvoice: %w\nstdout: %s\nstderr: %s", errExec, stdout, stderr)
	}

	// Extract hash from stderr (SDK logs go to stderr)
	combined := stdout + "\n" + stderr
	for _, line := range strings.Split(combined, "\n") {
		if strings.Contains(line, "INVOICE HASH =") {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				hash = strings.TrimSpace(parts[1])
			}
		}
	}

	// Extract signed XML
	idx := strings.Index(stdout, "===SIGNED===")
	if idx >= 0 {
		signedXML = strings.TrimSpace(stdout[idx+len("===SIGNED==="):])
	}

	if signedXML == "" {
		return "", "", fmt.Errorf("SignInvoice: no signed XML in output")
	}

	return signedXML, hash, nil
}

// GenerateQR generates the QR code for a signed invoice.
func (s *SDK) GenerateQR(xmlContent string) (string, error) {
	script := fmt.Sprintf(`%s
cat > /tmp/invoice.xml << 'XMLEOF'
%s
XMLEOF
fatoora -qr -invoice /tmp/invoice.xml %s
`, s.credentialScript(), xmlContent, s.envFlag())

	stdout, stderr, err := s.localExec(script)
	combined := stdout + "\n" + stderr
	if err != nil {
		return "", fmt.Errorf("GenerateQR: %w\noutput: %s", err, combined)
	}

	for _, line := range strings.Split(combined, "\n") {
		if strings.Contains(line, "QR code =") {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				return strings.TrimSpace(parts[1]), nil
			}
		}
	}
	return "", fmt.Errorf("GenerateQR: could not find QR in output: %s", combined)
}

// GenerateAPIRequest generates the JSON API request for an invoice.
func (s *SDK) GenerateAPIRequest(xmlContent string) (string, error) {
	script := fmt.Sprintf(`%s
cat > /tmp/invoice.xml << 'XMLEOF'
%s
XMLEOF
fatoora -invoice /tmp/invoice.xml -invoiceRequest -apiRequest /tmp/api.json %s
cat /tmp/api.json
`, s.credentialScript(), xmlContent, s.envFlag())

	stdout, stderr, err := s.localExec(script)
	if err != nil {
		return "", fmt.Errorf("GenerateAPIRequest: %w\nstderr: %s", err, stderr)
	}

	// The JSON is in stdout after the SDK banner
	// Find the JSON object
	for _, line := range strings.Split(stdout, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "{") {
			return trimmed, nil
		}
	}
	return strings.TrimSpace(stdout), nil
}

// --- Internal ---

func (s *SDK) envFlag() string {
	switch s.env {
	case config.Sandbox:
		return "-nonprod"
	case config.Simulation:
		return "-sim"
	case config.Production:
		return ""
	default:
		return "-nonprod"
	}
}

func (s *SDK) localExec(script string) (stdout, stderr string, err error) {
	cmd := exec.Command("bash", "-c", script)

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	err = cmd.Run()
	return outBuf.String(), errBuf.String(), err
}

func parseCSROutput(output string) (csr, key string, err error) {
	csrIdx := strings.Index(output, "===CSR===")
	keyIdx := strings.Index(output, "===KEY===")

	if csrIdx < 0 || keyIdx < 0 {
		return "", "", fmt.Errorf("missing CSR or KEY markers in output")
	}

	csr = strings.TrimSpace(output[csrIdx+len("===CSR===") : keyIdx])
	key = strings.TrimSpace(output[keyIdx+len("===KEY==="):])

	return csr, key, nil
}
