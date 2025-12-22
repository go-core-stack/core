// Copyright © 2025 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package certmanager

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/go-core-stack/core/errors"
)

func TestCertificateAuthorityProviderSignAndValidate(t *testing.T) {
	_, _, rootCertPEM, rootKeyPEM := createRootCA(t, time.Now().Add(6*time.Hour))

	providerName := "provider-" + t.Name()
	caProvider, err := InitializeCertificateAuthority(providerName, rootCertPEM, rootKeyPEM)
	if err != nil {
		t.Fatalf("initialize provider: %v", err)
	}

	fetched, err := GetCertificateAuthority(providerName)
	if err != nil {
		t.Fatalf("get provider: %v", err)
	}

	if fetched.RootCertificate() == nil || caProvider.RootCertificate() == nil {
		t.Fatalf("root certificate missing")
	}

	leafKey := mustGenerateKey(t)
	claims := Claims{
		Subject:        pkix.Name{CommonName: "leaf-cert"},
		DNSNames:       []string{"example.com"},
		IPAddresses:    []net.IP{net.ParseIP("127.0.0.1")},
		EmailAddresses: []string{"user@example.com"},
		KeyUsage:       x509.KeyUsageDigitalSignature,
		ExtKeyUsage:    []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		DynamicValues: map[string]any{
			"role":    "client",
			"ttl":     3600,
			"enabled": true,
		},
	}

	expiry := time.Now().Add(2 * time.Hour)
	signed, err := caProvider.SignWithPrivateKey(leafKey, expiry, claims)
	if err != nil {
		t.Fatalf("sign with private key: %v", err)
	}

	if signed.Certificate == nil || len(signed.PEM) == 0 {
		t.Fatalf("signed certificate is empty")
	}

	details, err := caProvider.ValidateCertificate(signed.Certificate, time.Now().Add(5*time.Minute))
	if err != nil {
		t.Fatalf("validate certificate: %v", err)
	}

	if details.Claims.Subject.CommonName != "leaf-cert" {
		t.Fatalf("subject mismatch: %s", details.Claims.Subject.CommonName)
	}

	if len(details.Claims.DNSNames) != 1 || details.Claims.DNSNames[0] != "example.com" {
		t.Fatalf("dns names mismatch: %v", details.Claims.DNSNames)
	}

	if len(details.Claims.IPAddresses) != 1 || !details.Claims.IPAddresses[0].Equal(net.ParseIP("127.0.0.1")) {
		t.Fatalf("ip addresses mismatch: %v", details.Claims.IPAddresses)
	}

	if len(details.Claims.ExtKeyUsage) != 1 || details.Claims.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth {
		t.Fatalf("ext key usage mismatch: %v", details.Claims.ExtKeyUsage)
	}

	if details.NotAfter.IsZero() || details.NotAfter.Sub(expiry) > time.Second || expiry.Sub(details.NotAfter) > time.Second {
		t.Fatalf("expiry mismatch: %v", details.NotAfter)
	}

	if len(details.Claims.DynamicValues) != 3 {
		t.Fatalf("dynamic values missing: %v", details.Claims.DynamicValues)
	}

	if details.Claims.DynamicValues["role"] != "client" {
		t.Fatalf("dynamic values role mismatch: %v", details.Claims.DynamicValues["role"])
	}

	if val, ok := details.Claims.DynamicValues["ttl"].(json.Number); !ok || val.String() != "3600" {
		t.Fatalf("dynamic values ttl mismatch: %v", details.Claims.DynamicValues["ttl"])
	}

	if val, ok := details.Claims.DynamicValues["enabled"].(bool); !ok || !val {
		t.Fatalf("dynamic values enabled mismatch: %v", details.Claims.DynamicValues["enabled"])
	}
}

func TestCertificateAuthoritySignWithCSRUsesCSRFields(t *testing.T) {
	_, _, rootCertPEM, rootKeyPEM := createRootCA(t, time.Now().Add(4*time.Hour))
	ca, err := NewCertificateAuthorityFromPEM(rootCertPEM, rootKeyPEM)
	if err != nil {
		t.Fatalf("new authority: %v", err)
	}

	leafKey := mustGenerateKey(t)
	csr := createCSR(t, leafKey, "csr-subject", []string{"csr.example.com"})

	expiry := time.Now().Add(90 * time.Minute)
	signed, err := ca.SignWithCSR(csr, expiry, Claims{})
	if err != nil {
		t.Fatalf("sign with csr: %v", err)
	}

	if signed.Certificate.Subject.CommonName != "csr-subject" {
		t.Fatalf("csr subject not applied: %s", signed.Certificate.Subject.CommonName)
	}

	if len(signed.Certificate.DNSNames) != 1 || signed.Certificate.DNSNames[0] != "csr.example.com" {
		t.Fatalf("csr dns names not applied: %v", signed.Certificate.DNSNames)
	}
}

func TestCertificateAuthorityRejectsMismatchedKey(t *testing.T) {
	_, _, rootCertPEM, _ := createRootCA(t, time.Now().Add(4*time.Hour))
	otherKey := mustGenerateKey(t)
	otherKeyPEM := encodePKCS8Key(t, otherKey)

	_, err := NewCertificateAuthorityFromPEM(rootCertPEM, otherKeyPEM)
	if err == nil || !errors.IsInvalidArgument(err) {
		t.Fatalf("expected invalid argument error, got: %v", err)
	}
}

func TestCertificateAuthorityRejectsExpiryBeyondRoot(t *testing.T) {
	_, _, rootCertPEM, rootKeyPEM := createRootCA(t, time.Now().Add(30*time.Minute))
	ca, err := NewCertificateAuthorityFromPEM(rootCertPEM, rootKeyPEM)
	if err != nil {
		t.Fatalf("new authority: %v", err)
	}

	leafKey := mustGenerateKey(t)
	expiry := time.Now().Add(2 * time.Hour)
	_, err = ca.SignWithPrivateKey(leafKey, expiry, Claims{})
	if err == nil || !errors.IsInvalidArgument(err) {
		t.Fatalf("expected invalid argument error, got: %v", err)
	}
}

func TestCertificateAuthorityValidateRejectsOtherRoot(t *testing.T) {
	_, _, rootCertPEM, rootKeyPEM := createRootCA(t, time.Now().Add(4*time.Hour))
	ca1, err := NewCertificateAuthorityFromPEM(rootCertPEM, rootKeyPEM)
	if err != nil {
		t.Fatalf("new authority: %v", err)
	}

	_, _, otherRootCertPEM, otherRootKeyPEM := createRootCA(t, time.Now().Add(4*time.Hour))
	ca2, err := NewCertificateAuthorityFromPEM(otherRootCertPEM, otherRootKeyPEM)
	if err != nil {
		t.Fatalf("new authority: %v", err)
	}

	leafKey := mustGenerateKey(t)
	expiry := time.Now().Add(1 * time.Hour)
	signed, err := ca1.SignWithPrivateKey(leafKey, expiry, Claims{})
	if err != nil {
		t.Fatalf("sign with private key: %v", err)
	}

	_, err = ca2.ValidateCertificate(signed.Certificate, time.Now().Add(5*time.Minute))
	if err == nil || !errors.IsInvalidArgument(err) {
		t.Fatalf("expected invalid argument error, got: %v", err)
	}
}

func TestCertificateAuthorityRejectsExpiredCertificate(t *testing.T) {
	_, _, rootCertPEM, rootKeyPEM := createRootCA(t, time.Now().Add(4*time.Hour))
	ca, err := NewCertificateAuthorityFromPEM(rootCertPEM, rootKeyPEM)
	if err != nil {
		t.Fatalf("new authority: %v", err)
	}

	leafKey := mustGenerateKey(t)
	claims := Claims{
		Subject:  pkix.Name{CommonName: "expired-cert"},
		DNSNames: []string{"expired.example.com"},
	}

	// Sign a certificate that expires in 1 hour
	expiry := time.Now().Add(1 * time.Hour)
	signed, err := ca.SignWithPrivateKey(leafKey, expiry, claims)
	if err != nil {
		t.Fatalf("sign with private key: %v", err)
	}

	// Validate at current time (should succeed)
	_, err = ca.ValidateCertificate(signed.Certificate, time.Now())
	if err != nil {
		t.Fatalf("validate current certificate should succeed: %v", err)
	}

	// Validate at a time after expiry (should fail)
	futureTime := time.Now().Add(2 * time.Hour)
	_, err = ca.ValidateCertificate(signed.Certificate, futureTime)
	if err == nil || !errors.IsInvalidArgument(err) {
		t.Fatalf("expected invalid argument error for expired certificate, got: %v", err)
	}
}

func TestCertificateAuthorityParseAndValidateCertificatePEM(t *testing.T) {
	_, _, rootCertPEM, rootKeyPEM := createRootCA(t, time.Now().Add(4*time.Hour))
	ca, err := NewCertificateAuthorityFromPEM(rootCertPEM, rootKeyPEM)
	if err != nil {
		t.Fatalf("new authority: %v", err)
	}

	leafKey := mustGenerateKey(t)
	claims := Claims{
		Subject:  pkix.Name{CommonName: "convenience-test"},
		DNSNames: []string{"test.example.com"},
		DynamicValues: map[string]any{
			"user_id": "12345",
		},
	}

	expiry := time.Now().Add(1 * time.Hour)
	signed, err := ca.SignWithPrivateKey(leafKey, expiry, claims)
	if err != nil {
		t.Fatalf("sign with private key: %v", err)
	}

	// Test the convenience method
	details, err := ca.ParseAndValidateCertificatePEM(signed.PEM)
	if err != nil {
		t.Fatalf("parse and validate certificate: %v", err)
	}

	if details.Claims.Subject.CommonName != "convenience-test" {
		t.Fatalf("subject mismatch: %s", details.Claims.Subject.CommonName)
	}

	if len(details.Claims.DNSNames) != 1 || details.Claims.DNSNames[0] != "test.example.com" {
		t.Fatalf("dns names mismatch: %v", details.Claims.DNSNames)
	}

	if details.Claims.DynamicValues["user_id"] != "12345" {
		t.Fatalf("dynamic values mismatch: %v", details.Claims.DynamicValues)
	}
}

func TestCertificateAuthoritySignCSRFromPEM(t *testing.T) {
	_, _, rootCertPEM, rootKeyPEM := createRootCA(t, time.Now().Add(4*time.Hour))
	ca, err := NewCertificateAuthorityFromPEM(rootCertPEM, rootKeyPEM)
	if err != nil {
		t.Fatalf("new authority: %v", err)
	}

	leafKey := mustGenerateKey(t)
	csr := createCSR(t, leafKey, "csr-convenience", []string{"csr.test.com"})

	// Encode CSR to PEM
	csrPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE REQUEST",
		Bytes: csr.Raw,
	})

	claims := Claims{
		DynamicValues: map[string]any{
			"department": "engineering",
		},
	}

	expiry := time.Now().Add(2 * time.Hour)
	signed, err := ca.SignCSRFromPEM(csrPEM, expiry, claims)
	if err != nil {
		t.Fatalf("sign csr from pem: %v", err)
	}

	if signed.Certificate.Subject.CommonName != "csr-convenience" {
		t.Fatalf("csr subject not applied: %s", signed.Certificate.Subject.CommonName)
	}

	if len(signed.Certificate.DNSNames) != 1 || signed.Certificate.DNSNames[0] != "csr.test.com" {
		t.Fatalf("csr dns names not applied: %v", signed.Certificate.DNSNames)
	}

	// Validate the signed certificate and check dynamic values
	details, err := ca.ValidateCertificate(signed.Certificate, time.Now())
	if err != nil {
		t.Fatalf("validate certificate: %v", err)
	}

	if details.Claims.DynamicValues["department"] != "engineering" {
		t.Fatalf("dynamic values not applied: %v", details.Claims.DynamicValues)
	}
}

func createRootCA(t *testing.T, notAfter time.Time) (*x509.Certificate, *rsa.PrivateKey, []byte, []byte) {
	t.Helper()

	key := mustGenerateKey(t)
	serial := mustGenerateSerial(t)

	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "root-ca"},
		NotBefore:             time.Now().Add(-1 * time.Minute),
		NotAfter:              notAfter,
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create root certificate: %v", err)
	}

	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse root certificate: %v", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: der,
	})

	keyPEM := encodePKCS8Key(t, key)

	return cert, key, certPEM, keyPEM
}

func createCSR(t *testing.T, key *rsa.PrivateKey, commonName string, dnsNames []string) *x509.CertificateRequest {
	t.Helper()

	template := &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: commonName},
		DNSNames: append([]string(nil), dnsNames...),
	}

	der, err := x509.CreateCertificateRequest(rand.Reader, template, key)
	if err != nil {
		t.Fatalf("create csr: %v", err)
	}

	csr, err := x509.ParseCertificateRequest(der)
	if err != nil {
		t.Fatalf("parse csr: %v", err)
	}

	return csr
}

func mustGenerateKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	return key
}

func mustGenerateSerial(t *testing.T) *big.Int {
	t.Helper()

	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, limit)
	if err != nil {
		t.Fatalf("generate serial: %v", err)
	}

	if serial.Sign() <= 0 {
		t.Fatalf("serial number is not positive")
	}

	return serial
}

func encodePKCS8Key(t *testing.T, key *rsa.PrivateKey) []byte {
	t.Helper()

	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal pkcs8 key: %v", err)
	}

	return pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: keyDER,
	})
}
