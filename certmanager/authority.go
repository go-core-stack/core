// Copyright © 2025-2026 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

// Package certmanager provides a certificate authority implementation for signing
// and validating X.509 certificates, with special support for mTLS connections.
//
// This package enables applications to issue short-lived certificates with embedded
// metadata (dynamic values) that can be used for application-level authorization,
// tenant identification, or context propagation during mTLS handshakes.
//
// # Key Features
//
//   - Certificate signing using private keys or CSRs
//   - Dynamic values extension for embedding custom claims in certificates
//   - Certificate validation against a root CA
//   - Support for RSA, ECDSA, and Ed25519 key types
//   - Provider registry for managing multiple certificate authorities
//
// # Usage Example
//
//	// Initialize a certificate authority
//	ca, err := certmanager.InitializeCertificateAuthority("my-ca", rootCertPEM, rootKeyPEM)
//	if err != nil {
//	    log.Fatal(err)
//	}
//
//	// Sign a certificate with dynamic values for mTLS
//	claims := certmanager.Claims{
//	    Subject: pkix.Name{CommonName: "client-app"},
//	    DNSNames: []string{"client.example.com"},
//	    DynamicValues: map[string]any{
//	        "tenant_id": "acme-corp",
//	        "role": "admin",
//	        "permissions": []string{"read", "write"},
//	    },
//	}
//	signed, err := ca.SignWithPrivateKey(clientKey, time.Now().Add(24*time.Hour), claims)
//
//	// Or sign from a PEM-encoded CSR (convenience method)
//	signed, err = ca.SignCSRFromPEM(csrPEM, time.Now().Add(24*time.Hour), claims)
//
//	// Later, during mTLS connection, validate and extract claims
//	details, err := ca.ParseAndValidateCertificatePEM(certPEM) // convenience method
//	if err != nil {
//	    // Certificate is invalid or expired
//	}
//	tenantID := details.Claims.DynamicValues["tenant_id"]
//
// # Dynamic Values for mTLS
//
// The package supports embedding arbitrary JSON-encodable data into certificates
// via the DynamicValues field. This data is stored in a custom X.509 extension
// and can be extracted during certificate validation, enabling rich authorization
// contexts in mTLS connections without requiring external lookups.
package certmanager

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/url"
	"reflect"
	"sync"
	"time"

	"github.com/go-core-stack/core/errors"
)

// Claims define additional fields that can be embedded into a certificate.
type Claims struct {
	Subject        pkix.Name
	DNSNames       []string
	IPAddresses    []net.IP
	EmailAddresses []string
	URIs           []*url.URL
	KeyUsage       x509.KeyUsage
	ExtKeyUsage    []x509.ExtKeyUsage
	IsCA           bool
	SerialNumber   *big.Int
	NotBefore      time.Time
	DynamicValues  map[string]any
	Extensions     []pkix.Extension
}

// SignedCertificate provides the signed certificate and PEM data.
type SignedCertificate struct {
	Certificate *x509.Certificate
	PEM         []byte
}

// CertificateDetails provides certificate claims and details after validation.
type CertificateDetails struct {
	Claims             Claims
	Issuer             pkix.Name
	SerialNumber       *big.Int
	NotBefore          time.Time
	NotAfter           time.Time
	PublicKeyAlgorithm x509.PublicKeyAlgorithm
	SignatureAlgorithm x509.SignatureAlgorithm
}

// Provider defines interface for a certificate authority provider.
type Provider interface {
	SignWithPrivateKey(privateKey crypto.PrivateKey, expiry time.Time, claims Claims) (*SignedCertificate, error)
	SignWithCSR(csr *x509.CertificateRequest, expiry time.Time, claims Claims) (*SignedCertificate, error)
	ValidateCertificate(cert *x509.Certificate, at time.Time) (*CertificateDetails, error)
	RootCertificate() *x509.Certificate
	ParseAndValidateCertificatePEM(certPEM []byte) (*CertificateDetails, error)
	SignCSRFromPEM(csrPEM []byte, expiry time.Time, claims Claims) (*SignedCertificate, error)
}

// CertificateAuthority provides signing and validation using a root CA.
type CertificateAuthority struct {
	rootCert *x509.Certificate
	rootKey  crypto.Signer
}

var (
	caProviders     = make(map[string]Provider)
	caProvidersLock sync.RWMutex
)

// DefaultDynamicValuesOID identifies the X.509 extension used for dynamic claims.
// This OID is reserved for internal use within this package and is specifically designed
// to carry additional information for clients during mTLS connections. The extension
// contains JSON-encoded key-value pairs that can be extracted and used for application-level
// authorization, metadata, or context propagation.
//
// OID Structure: 1.3.6.1.4.1.98765.1.1
//   - 1.3.6.1.4.1 = Private Enterprise Numbers (IANA)
//   - 98765 = Placeholder PEN (not officially registered)
//   - 1.1 = Dynamic values extension
//
// IMPORTANT: For production use, you should either:
//   1. Register your own Private Enterprise Number (PEN) with IANA at:
//      https://www.iana.org/assignments/enterprise-numbers/
//   2. Use your organization's existing PEN if available
//   3. Create a custom OID by replacing DefaultDynamicValuesOID with your own asn1.ObjectIdentifier
//
// The current PEN (98765) is a placeholder to avoid conflicts with registered numbers
// like Sigstore (57264), and should be replaced before production deployment.
var DefaultDynamicValuesOID = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 98765, 1, 1}

// InitializeCertificateAuthority initializes a provider using root CA PEM data.
func InitializeCertificateAuthority(provider string, rootCertPEM, rootKeyPEM []byte) (Provider, error) {
	if provider == "" {
		return nil, errors.Wrap(errors.InvalidArgument, "provider is required")
	}

	caProvidersLock.Lock()
	defer caProvidersLock.Unlock()

	if caProviders[provider] != nil {
		return nil, errors.Wrap(errors.AlreadyExists, "certificate authority provider already exists")
	}

	ca, err := NewCertificateAuthorityFromPEM(rootCertPEM, rootKeyPEM)
	if err != nil {
		return nil, err
	}

	caProviders[provider] = ca
	return ca, nil
}

// GetCertificateAuthority returns a previously initialized provider.
func GetCertificateAuthority(provider string) (Provider, error) {
	caProvidersLock.RLock()
	defer caProvidersLock.RUnlock()

	ca, ok := caProviders[provider]
	if !ok {
		return nil, errors.Wrap(errors.NotFound, "certificate authority provider not found")
	}

	return ca, nil
}

// NewCertificateAuthority creates a new certificate authority using parsed data.
func NewCertificateAuthority(rootCert *x509.Certificate, rootKey crypto.Signer) (*CertificateAuthority, error) {
	if rootCert == nil {
		return nil, errors.Wrap(errors.InvalidArgument, "root certificate is required")
	}

	if rootKey == nil {
		return nil, errors.Wrap(errors.InvalidArgument, "root key is required")
	}

	if !rootCert.IsCA {
		return nil, errors.Wrap(errors.InvalidArgument, "root certificate is not a CA certificate")
	}

	if rootCert.KeyUsage != 0 && rootCert.KeyUsage&x509.KeyUsageCertSign == 0 {
		return nil, errors.Wrap(errors.InvalidArgument, "root certificate is not allowed to sign certificates")
	}

	if !publicKeysMatch(rootCert.PublicKey, rootKey.Public()) {
		return nil, errors.Wrap(errors.InvalidArgument, "root certificate does not match the provided key")
	}

	return &CertificateAuthority{
		rootCert: rootCert,
		rootKey:  rootKey,
	}, nil
}

// NewCertificateAuthorityFromPEM creates a new certificate authority using PEM data.
func NewCertificateAuthorityFromPEM(rootCertPEM, rootKeyPEM []byte) (*CertificateAuthority, error) {
	rootCert, err := ParseCertificatePEM(rootCertPEM)
	if err != nil {
		return nil, err
	}

	rootKey, err := ParsePrivateKeyPEM(rootKeyPEM)
	if err != nil {
		return nil, err
	}

	signer, ok := rootKey.(crypto.Signer)
	if !ok {
		return nil, errors.Wrap(errors.InvalidArgument, "root key does not implement crypto.Signer")
	}

	return NewCertificateAuthority(rootCert, signer)
}

// RootCertificate returns the root certificate for this authority.
func (ca *CertificateAuthority) RootCertificate() *x509.Certificate {
	return ca.rootCert
}

// ParseAndValidateCertificatePEM parses a PEM-encoded certificate and validates it
// against this authority at the current time. This is a convenience method that
// combines ParseCertificatePEM and ValidateCertificate.
func (ca *CertificateAuthority) ParseAndValidateCertificatePEM(certPEM []byte) (*CertificateDetails, error) {
	cert, err := ParseCertificatePEM(certPEM)
	if err != nil {
		return nil, err
	}

	return ca.ValidateCertificate(cert, time.Now())
}

// SignCSRFromPEM parses a PEM-encoded CSR and signs it with the specified expiry
// and claims. This is a convenience method that combines ParseCertificateRequestPEM
// and SignWithCSR.
func (ca *CertificateAuthority) SignCSRFromPEM(csrPEM []byte, expiry time.Time, claims Claims) (*SignedCertificate, error) {
	csr, err := ParseCertificateRequestPEM(csrPEM)
	if err != nil {
		return nil, err
	}

	return ca.SignWithCSR(csr, expiry, claims)
}

// SignWithPrivateKey signs a certificate using the provided private key.
func (ca *CertificateAuthority) SignWithPrivateKey(privateKey crypto.PrivateKey, expiry time.Time, claims Claims) (*SignedCertificate, error) {
	if privateKey == nil {
		return nil, errors.Wrap(errors.InvalidArgument, "private key is required")
	}

	signer, ok := privateKey.(crypto.Signer)
	if !ok {
		return nil, errors.Wrap(errors.InvalidArgument, "private key does not implement crypto.Signer")
	}

	return ca.signWithPublicKey(signer.Public(), expiry, claims)
}

// SignWithCSR signs a certificate using the provided CSR.
func (ca *CertificateAuthority) SignWithCSR(csr *x509.CertificateRequest, expiry time.Time, claims Claims) (*SignedCertificate, error) {
	if csr == nil {
		return nil, errors.Wrap(errors.InvalidArgument, "CSR is required")
	}

	if err := csr.CheckSignature(); err != nil {
		return nil, errors.Wrap(errors.InvalidArgument, "CSR signature validation failed: "+err.Error())
	}

	mergedClaims := mergeClaimsWithCSR(claims, csr)
	return ca.signWithPublicKey(csr.PublicKey, expiry, mergedClaims)
}

// ValidateCertificate validates a certificate and returns its claims and details.
func (ca *CertificateAuthority) ValidateCertificate(cert *x509.Certificate, at time.Time) (*CertificateDetails, error) {
	if cert == nil {
		return nil, errors.Wrap(errors.InvalidArgument, "certificate is required")
	}

	if at.IsZero() {
		at = time.Now()
	}

	pool := x509.NewCertPool()
	pool.AddCert(ca.rootCert)

	opts := x509.VerifyOptions{
		Roots:       pool,
		CurrentTime: at,
		KeyUsages:   []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	}

	if _, err := cert.Verify(opts); err != nil {
		return nil, errors.Wrap(errors.InvalidArgument, "certificate validation failed: "+err.Error())
	}

	// TODO: handle certificate revocation separately.

	claims, err := claimsFromCertificate(cert)
	if err != nil {
		return nil, err
	}

	return &CertificateDetails{
		Claims:             claims,
		Issuer:             cert.Issuer,
		SerialNumber:       cert.SerialNumber,
		NotBefore:          cert.NotBefore,
		NotAfter:           cert.NotAfter,
		PublicKeyAlgorithm: cert.PublicKeyAlgorithm,
		SignatureAlgorithm: cert.SignatureAlgorithm,
	}, nil
}

// ParseCertificatePEM parses a PEM encoded certificate.
func ParseCertificatePEM(certPEM []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.Wrap(errors.InvalidArgument, "invalid certificate PEM")
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, errors.Wrap(errors.InvalidArgument, "failed to parse certificate: "+err.Error())
	}

	return cert, nil
}

// ParseCertificateRequestPEM parses a PEM encoded CSR.
func ParseCertificateRequestPEM(csrPEM []byte) (*x509.CertificateRequest, error) {
	block, _ := pem.Decode(csrPEM)
	if block == nil || (block.Type != "CERTIFICATE REQUEST" && block.Type != "NEW CERTIFICATE REQUEST") {
		return nil, errors.Wrap(errors.InvalidArgument, "invalid CSR PEM")
	}

	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, errors.Wrap(errors.InvalidArgument, "failed to parse CSR: "+err.Error())
	}

	return csr, nil
}

// ParsePrivateKeyPEM parses a PEM encoded private key.
func ParsePrivateKeyPEM(keyPEM []byte) (crypto.PrivateKey, error) {
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, errors.Wrap(errors.InvalidArgument, "invalid private key PEM")
	}

	switch block.Type {
	case "PRIVATE KEY":
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, errors.Wrap(errors.InvalidArgument, "failed to parse PKCS8 key: "+err.Error())
		}
		return key, nil
	case "RSA PRIVATE KEY":
		key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return nil, errors.Wrap(errors.InvalidArgument, "failed to parse RSA key: "+err.Error())
		}
		return key, nil
	case "EC PRIVATE KEY":
		key, err := x509.ParseECPrivateKey(block.Bytes)
		if err != nil {
			return nil, errors.Wrap(errors.InvalidArgument, "failed to parse EC key: "+err.Error())
		}
		return key, nil
	default:
		return nil, errors.Wrap(errors.InvalidArgument, "unsupported private key type: "+block.Type)
	}
}

func (ca *CertificateAuthority) signWithPublicKey(publicKey crypto.PublicKey, expiry time.Time, claims Claims) (*SignedCertificate, error) {
	if publicKey == nil {
		return nil, errors.Wrap(errors.InvalidArgument, "public key is required")
	}

	if expiry.IsZero() {
		return nil, errors.Wrap(errors.InvalidArgument, "expiry time is required")
	}

	notBefore := time.Now()
	if !claims.NotBefore.IsZero() {
		notBefore = claims.NotBefore
	}

	if notBefore.Before(ca.rootCert.NotBefore) {
		notBefore = ca.rootCert.NotBefore
	}

	if expiry.After(ca.rootCert.NotAfter) {
		return nil, errors.Wrap(errors.InvalidArgument, "expiry time exceeds root certificate validity")
	}

	if !expiry.After(notBefore) {
		return nil, errors.Wrap(errors.InvalidArgument, "expiry time must be after not-before time")
	}

	template, err := buildCertificateTemplate(claims, notBefore, expiry)
	if err != nil {
		return nil, err
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, template, ca.rootCert, publicKey, ca.rootKey)
	if err != nil {
		return nil, errors.Wrap(errors.Unknown, "failed to sign certificate: "+err.Error())
	}

	cert, err := x509.ParseCertificate(derBytes)
	if err != nil {
		return nil, errors.Wrap(errors.Unknown, "failed to parse signed certificate: "+err.Error())
	}

	certPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: derBytes,
	})

	return &SignedCertificate{
		Certificate: cert,
		PEM:         certPEM,
	}, nil
}

func buildCertificateTemplate(claims Claims, notBefore, notAfter time.Time) (*x509.Certificate, error) {
	if !notAfter.After(notBefore) {
		return nil, errors.Wrap(errors.InvalidArgument, "invalid certificate validity range")
	}

	serialNumber := claims.SerialNumber
	if serialNumber == nil {
		var err error
		serialNumber, err = randomSerialNumber()
		if err != nil {
			return nil, err
		}
	}

	keyUsage := claims.KeyUsage
	if keyUsage == 0 {
		keyUsage = x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment
	}

	if claims.IsCA {
		keyUsage |= x509.KeyUsageCertSign
	}

	extraExtensions := append([]pkix.Extension(nil), claims.Extensions...)
	if claims.DynamicValues != nil {
		if extensionsContainOID(extraExtensions, DefaultDynamicValuesOID) {
			return nil, errors.Wrap(errors.InvalidArgument, "dynamic values extension already present")
		}
		encoded, err := encodeDynamicValues(claims.DynamicValues)
		if err != nil {
			return nil, err
		}
		extraExtensions = append(extraExtensions, pkix.Extension{
			Id:    DefaultDynamicValuesOID,
			Value: encoded,
		})
	}

	return &x509.Certificate{
		SerialNumber:          serialNumber,
		Subject:               claims.Subject,
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              keyUsage,
		ExtKeyUsage:           append([]x509.ExtKeyUsage(nil), claims.ExtKeyUsage...),
		IsCA:                  claims.IsCA,
		BasicConstraintsValid: true,
		DNSNames:              append([]string(nil), claims.DNSNames...),
		IPAddresses:           append([]net.IP(nil), claims.IPAddresses...),
		EmailAddresses:        append([]string(nil), claims.EmailAddresses...),
		URIs:                  append([]*url.URL(nil), claims.URIs...),
		ExtraExtensions:       extraExtensions,
	}, nil
}

func mergeClaimsWithCSR(claims Claims, csr *x509.CertificateRequest) Claims {
	if isZeroSubject(claims.Subject) {
		claims.Subject = csr.Subject
	}

	if len(claims.DNSNames) == 0 {
		claims.DNSNames = append([]string(nil), csr.DNSNames...)
	}

	if len(claims.IPAddresses) == 0 {
		claims.IPAddresses = append([]net.IP(nil), csr.IPAddresses...)
	}

	if len(claims.EmailAddresses) == 0 {
		claims.EmailAddresses = append([]string(nil), csr.EmailAddresses...)
	}

	if len(claims.URIs) == 0 {
		claims.URIs = append([]*url.URL(nil), csr.URIs...)
	}

	return claims
}

func isZeroSubject(subject pkix.Name) bool {
	empty := pkix.Name{}
	return reflect.DeepEqual(subject, empty)
}

func claimsFromCertificate(cert *x509.Certificate) (Claims, error) {
	dynamicValues, err := extractDynamicValues(cert.Extensions)
	if err != nil {
		return Claims{}, err
	}

	return Claims{
		Subject:        cert.Subject,
		DNSNames:       append([]string(nil), cert.DNSNames...),
		IPAddresses:    append([]net.IP(nil), cert.IPAddresses...),
		EmailAddresses: append([]string(nil), cert.EmailAddresses...),
		URIs:           append([]*url.URL(nil), cert.URIs...),
		KeyUsage:       cert.KeyUsage,
		ExtKeyUsage:    append([]x509.ExtKeyUsage(nil), cert.ExtKeyUsage...),
		IsCA:           cert.IsCA,
		SerialNumber:   cert.SerialNumber,
		NotBefore:      cert.NotBefore,
		DynamicValues:  dynamicValues,
		Extensions:     append([]pkix.Extension(nil), cert.Extensions...),
	}, nil
}

func randomSerialNumber() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, errors.Wrap(errors.Unknown, "failed to generate serial number: "+err.Error())
	}
	// Ensure serial is positive by adding 1 (shifts range from [0, 2^128) to [1, 2^128])
	return serial.Add(serial, big.NewInt(1)), nil
}

func publicKeysMatch(a, b crypto.PublicKey) bool {
	// Marshal both keys to PKIX format for comparison
	// This ensures different in-memory representations of the same key are considered equal
	aBytes, err := x509.MarshalPKIXPublicKey(a)
	if err != nil {
		return false
	}
	bBytes, err := x509.MarshalPKIXPublicKey(b)
	if err != nil {
		return false
	}
	return bytes.Equal(aBytes, bBytes)
}

func encodeDynamicValues(values map[string]any) ([]byte, error) {
	payload, err := json.Marshal(values)
	if err != nil {
		return nil, errors.Wrap(errors.InvalidArgument, "failed to encode dynamic values: "+err.Error())
	}

	encoded, err := asn1.Marshal(payload)
	if err != nil {
		return nil, errors.Wrap(errors.InvalidArgument, "failed to encode dynamic values extension: "+err.Error())
	}

	return encoded, nil
}

func decodeDynamicValues(encoded []byte) (map[string]any, error) {
	var payload []byte
	if _, err := asn1.Unmarshal(encoded, &payload); err != nil {
		return nil, errors.Wrap(errors.InvalidArgument, "failed to decode dynamic values extension: "+err.Error())
	}

	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()

	var values map[string]any
	if err := decoder.Decode(&values); err != nil {
		return nil, errors.Wrap(errors.InvalidArgument, "failed to decode dynamic values: "+err.Error())
	}

	return values, nil
}

func extractDynamicValues(extensions []pkix.Extension) (map[string]any, error) {
	value, ok := findExtensionValue(extensions, DefaultDynamicValuesOID)
	if !ok {
		return nil, nil
	}

	return decodeDynamicValues(value)
}

func findExtensionValue(extensions []pkix.Extension, oid asn1.ObjectIdentifier) ([]byte, bool) {
	for _, ext := range extensions {
		if oidEqual(ext.Id, oid) {
			return ext.Value, true
		}
	}
	return nil, false
}

func extensionsContainOID(extensions []pkix.Extension, oid asn1.ObjectIdentifier) bool {
	_, ok := findExtensionValue(extensions, oid)
	return ok
}

func oidEqual(a, b asn1.ObjectIdentifier) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
