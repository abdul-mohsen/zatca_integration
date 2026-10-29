package invoice

import (
	"bytes"
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"strings"
)

// ImportFailureClass is the stable machine-readable classification for an
// import outcome that cannot be treated as a clean canonical invoice.
type ImportFailureClass string

const (
	FailureClassNone             ImportFailureClass = ""
	FailureClassMalformed        ImportFailureClass = "malformed"
	FailureClassUnsafe           ImportFailureClass = "unsafe"
	FailureClassOversized        ImportFailureClass = "oversized"
	FailureClassRequiredFields   ImportFailureClass = "required_fields"
	FailureClassSignature        ImportFailureClass = "signature"
	FailureClassTrustUnavailable ImportFailureClass = "trust_unavailable"
	FailureClassSchema           ImportFailureClass = "schema"
	FailureClassUnsupported      ImportFailureClass = "unsupported"

	// Compatibility aliases keep failure names discoverable to callers that
	// prefer the ImportFailure prefix.
	ImportFailureMalformed        = FailureClassMalformed
	ImportFailureUnsafe           = FailureClassUnsafe
	ImportFailureOversized        = FailureClassOversized
	ImportFailureRequiredFields   = FailureClassRequiredFields
	ImportFailureSignature        = FailureClassSignature
	ImportFailureTrustUnavailable = FailureClassTrustUnavailable
	ImportFailureSchema           = FailureClassSchema
	ImportFailureUnsupported      = FailureClassUnsupported
)

var (
	ErrImportMalformed        = errors.New("ubl import failed: malformed XML")
	ErrImportUnsafe           = errors.New("ubl import failed: unsafe XML")
	ErrImportOversized        = errors.New("ubl import failed: oversized XML")
	ErrImportRequiredFields   = errors.New("ubl import failed: required fiscal fields")
	ErrImportSignature        = errors.New("ubl import failed: invalid signature")
	ErrImportTrustUnavailable = errors.New("ubl import failed: trust unavailable")
	ErrImportSchema           = errors.New("ubl import failed: schema validation")
	ErrImportUnsupported      = errors.New("ubl import failed: unsupported UBL content")
)

// ImportRequest carries supplier bytes and their caller-supplied provenance.
// Bytes is the preferred field; Data and OriginalBytes are accepted aliases so
// adapters can pass through their existing naming without copying the data.
type ImportRequest struct {
	Bytes            []byte
	Data             []byte
	OriginalBytes    []byte
	MIMEType         string
	OriginalName     string
	ContentReference string
	Provenance       string
}

func (r ImportRequest) inputBytes() []byte {
	switch {
	case len(r.Bytes) > 0:
		return r.Bytes
	case len(r.Data) > 0:
		return r.Data
	default:
		return r.OriginalBytes
	}
}

// ImportResult contains the immutable in-memory evidence and canonical
// parser output. It does not persist the original bytes.
type ImportResult struct {
	OriginalBytes  []byte
	Source         XMLSourceEvidence
	Canonical      *UBLImport
	Parsed         *UBLImport
	FailureClass   ImportFailureClass
	FailureMessage string
	Failure        *ImportFailure `json:"-"`
}

// ImportFailure is a typed import error. Result remains available for failures
// discovered after safe XML parsing so callers never lose source evidence.
type ImportFailure struct {
	Class   ImportFailureClass
	Message string
	Cause   error
	Result  *ImportResult `json:"-"`
}

func (f *ImportFailure) Error() string {
	if f == nil {
		return ""
	}
	if f.Message == "" {
		return string(f.Class)
	}
	return fmt.Sprintf("%s: %s", f.Class, f.Message)
}

func (f *ImportFailure) Unwrap() error {
	if f == nil {
		return nil
	}
	return f.Cause
}

// Is lets callers use errors.Is with the stable class sentinel.
func (f *ImportFailure) Is(target error) bool {
	if f == nil {
		return false
	}
	return target == failureSentinel(f.Class)
}

// ImportService is the side-effect-free callable boundary for supplier UBL.
// It delegates XML mapping and cryptographic evidence collection to the
// existing ParseSignedUBL parser.
type ImportService struct {
	options ImportOptions
}

// ImportServiceOptions is an alias for parser options used by the service.
type ImportServiceOptions = ImportOptions

// NewImportService constructs a service with optional schema and trust
// validators. The options slice is copied so later caller mutations do not
// change the service's trust policy.
func NewImportService(options ImportOptions) *ImportService {
	options.TrustedCertificates = append([]*x509.Certificate(nil), options.TrustedCertificates...)
	return &ImportService{options: options}
}

// NewUBLImportService is an explicit alias for callers naming the document
// format in their dependency wiring.
func NewUBLImportService(options ImportOptions) *ImportService {
	return NewImportService(options)
}

// Import parses one UBL document and returns its exact bytes/provenance and
// canonical parsed data. Unsigned or untrusted-but-well-formed documents are
// returned for review with FailureClassTrustUnavailable; invalid signatures,
// required-field failures, schema failures, and unsupported fields return a
// typed error with the parsed evidence attached.
func (s *ImportService) Import(request ImportRequest) (*ImportResult, error) {
	return s.ImportContext(context.Background(), request)
}

// ImportSignedUBL is a descriptive method alias for Import.
func (s *ImportService) ImportSignedUBL(request ImportRequest) (*ImportResult, error) {
	return s.Import(request)
}

// ImportContext is the context-aware form used by adapters that already
// propagate cancellation. Parsing itself has no external I/O.
func (s *ImportService) ImportContext(ctx context.Context, request ImportRequest) (*ImportResult, error) {
	data := request.inputBytes()
	result := newImportResult(data, request)
	if ctx != nil {
		select {
		case <-ctx.Done():
			return result, newImportFailure(result, FailureClassMalformed, ctx.Err(), "import cancelled")
		default:
		}
	}

	options := s.options
	parsed, err := ParseSignedUBL(result.OriginalBytes, options)
	if err != nil {
		class := classifyParserFailure(err)
		return result, newImportFailure(result, class, err, err.Error())
	}

	parsed.Source = result.Source
	result.Canonical = parsed
	result.Parsed = parsed

	if class, message := classifyParsedFailure(parsed, options); class != FailureClassNone {
		result.FailureClass = class
		result.FailureMessage = message
		if class == FailureClassTrustUnavailable {
			// Trust is intentionally review-gated rather than silently
			// upgraded to a cryptographically verified import.
			result.Failure = &ImportFailure{
				Class:   class,
				Message: message,
				Result:  result,
			}
			return result, nil
		}
		return result, newImportFailure(result, class, nil, message)
	}

	return result, nil
}

// ImportSignedUBL provides a package-level convenience boundary for callers
// that do not need to retain a service instance.
func ImportSignedUBL(request ImportRequest) (*ImportResult, error) {
	return NewImportService(ImportOptions{}).Import(request)
}

func newImportResult(data []byte, request ImportRequest) *ImportResult {
	original := bytes.Clone(data)
	source := XMLSourceEvidence{
		ContentHash:      sha256Hex(original),
		Size:             int64(len(original)),
		MIMEType:         strings.TrimSpace(request.MIMEType),
		OriginalName:     request.OriginalName,
		ContentReference: request.ContentReference,
		Provenance:       strings.TrimSpace(request.Provenance),
	}
	if source.MIMEType == "" {
		source.MIMEType = "application/xml"
	}
	if source.Provenance == "" {
		source.Provenance = "supplier_xml"
	}
	return &ImportResult{
		OriginalBytes: original,
		Source:        source,
	}
}

func newImportFailure(result *ImportResult, class ImportFailureClass, cause error, message string) *ImportFailure {
	failure := &ImportFailure{
		Class:   class,
		Message: message,
		Cause:   cause,
		Result:  result,
	}
	result.FailureClass = class
	result.FailureMessage = message
	result.Failure = failure
	return failure
}

func classifyParserFailure(err error) ImportFailureClass {
	switch {
	case errors.Is(err, ErrUBLImportOversized):
		return FailureClassOversized
	case errors.Is(err, ErrUBLImportUnsafe):
		return FailureClassUnsafe
	case errors.Is(err, ErrUBLImportMalformed):
		return FailureClassMalformed
	default:
		return FailureClassMalformed
	}
}

func classifyParsedFailure(parsed *UBLImport, options ImportOptions) (ImportFailureClass, string) {
	if parsed == nil {
		return FailureClassMalformed, "parser returned no result"
	}
	if parsed.Signature.Status == SignatureStatusInvalid {
		message := parsed.Signature.VerificationMessage
		if message == "" {
			message = "signature evidence is invalid"
		}
		return FailureClassSignature, message
	}
	for _, issue := range parsed.Validation.Issues {
		if issue.Severity == "error" &&
			(strings.HasPrefix(issue.Code, "missing_") || strings.HasPrefix(issue.Code, "invalid_")) {
			return FailureClassRequiredFields, issue.Message
		}
	}
	if parsed.Validation.SchemaStatus == SchemaStatusInvalid {
		return FailureClassSchema, "UBL schema/profile validation failed"
	}
	if len(parsed.UnsupportedFields) > 0 {
		return FailureClassUnsupported, "UBL contains fields not supported by the canonical mapper"
	}
	if parsed.Signature.Status == SignatureStatusUnsigned ||
		(parsed.Signature.Status == SignatureStatusUnverified && len(options.TrustedCertificates) == 0) {
		if parsed.Signature.Status == SignatureStatusUnsigned {
			return FailureClassTrustUnavailable, "document has no signature to verify"
		}
		return FailureClassTrustUnavailable, "no trusted certificate roots were supplied"
	}
	return FailureClassNone, ""
}

func failureSentinel(class ImportFailureClass) error {
	switch class {
	case FailureClassMalformed:
		return ErrImportMalformed
	case FailureClassUnsafe:
		return ErrImportUnsafe
	case FailureClassOversized:
		return ErrImportOversized
	case FailureClassRequiredFields:
		return ErrImportRequiredFields
	case FailureClassSignature:
		return ErrImportSignature
	case FailureClassTrustUnavailable:
		return ErrImportTrustUnavailable
	case FailureClassSchema:
		return ErrImportSchema
	case FailureClassUnsupported:
		return ErrImportUnsupported
	default:
		return nil
	}
}
