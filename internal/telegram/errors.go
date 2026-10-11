package telegram

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"time"
)

// TransportError keeps request URLs and transport diagnostics out of logs while
// preserving the cause for cancellation and retry classification.
type TransportError struct {
	Err       error
	Retryable bool
}

func (e *TransportError) Error() string { return "telegram transport or request failure" }
func (e *TransportError) Unwrap() error { return e.Err }

func temporaryTransportError(err error) bool {
	var certificate *tls.CertificateVerificationError
	var unknownAuthority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalidCertificate x509.CertificateInvalidError
	var tlsHeader tls.RecordHeaderError
	if errors.As(err, &certificate) || errors.As(err, &unknownAuthority) ||
		errors.As(err, &hostname) || errors.As(err, &invalidCertificate) || errors.As(err, &tlsHeader) {
		return false
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return dns.Timeout() || dns.Temporary()
	}
	// HTTP/2 GOAWAY and stream errors need not implement net.Error. Once
	// request configuration and certificate errors are excluded, retry read
	// transport failures on a fresh attempt rather than exiting the worker.
	return true
}

// ClassifyError returns bounded operational reasons, never upstream text.
func ClassifyError(err error) (reason string, retryAfter time.Duration, retry bool) {
	var api *APIError
	if errors.As(err, &api) {
		code := api.ErrorCode
		if code == 0 {
			code = api.StatusCode
		}
		switch {
		case code == 401 || code == 403 || code == 404:
			return "credentials", 0, false
		case code == 409:
			return "conflict", 0, false
		case code == 429:
			if api.RetryAfter > 0 {
				// Saturate rather than wrapping a hostile upstream integer.
				seconds := min(int64(api.RetryAfter), int64((1<<63-1)/time.Second))
				return "rate_limited", time.Duration(seconds) * time.Second, true
			}
			return "rate_limited", 0, true
		case code >= 500 && code <= 599:
			return "server", 0, true
		default:
			return "permanent", 0, false
		}
	}
	var transport *TransportError
	if errors.As(err, &transport) {
		if transport.Retryable {
			return "transport", 0, true
		}
		return "configuration", 0, false
	}
	return "protocol", 0, false
}
