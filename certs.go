package puppy

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
    "os"
	"time"
)

// A certificate/private key pair
type CAKeyPair struct {
	Certificate []byte
	PrivateKey  *rsa.PrivateKey
}

func bigIntHash(n *big.Int) []byte {
	h := sha1.New()
	h.Write(n.Bytes())
	return h.Sum(nil)
}

// GenerateCACerts generates a random CAKeyPair
func GenerateCACerts() (*CAKeyPair, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("error generating private key: %s", err.Error())
	}

	serial := new(big.Int)
	b := make([]byte, 20)
	_, err = rand.Read(b)
	if err != nil {
		return nil, fmt.Errorf("error generating serial: %s", err.Error())
	}
	serial.SetBytes(b)

	end, err := time.Parse("2006-01-02", "2049-12-31")
	template := x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   "Puppy Proxy",
			Organization: []string{"Puppy Proxy"},
		},
		NotBefore: time.Now().Add(-5 * time.Minute).UTC(),
		NotAfter:  end,

		SubjectKeyId:          bigIntHash(key.N),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:           true,
		MaxPathLenZero: true,
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("error generating certificate: %s", err.Error())
	}

	return &CAKeyPair{
		Certificate: derBytes,
		PrivateKey:  key,
	}, nil
}

// Generate a pair of certificates and write them to the disk. Returns the generated keypair
func GenerateCACertsToDisk(CertificateFile string, PrivateKeyFile string) (*CAKeyPair, error) {
	pair, err := GenerateCACerts()
	if err != nil {
		return nil, err
	}

	pkeyFile, err := os.OpenFile(PrivateKeyFile, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, err
	}
	pkeyFile.Write(pair.PrivateKeyPEM())
	if err := pkeyFile.Close(); err != nil {
		return nil, err
	}

	certFile, err := os.OpenFile(CertificateFile, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, err
	}

	certFile.Write(pair.CACertPEM())
	if err := certFile.Close(); err != nil {
		return nil, err
	}

    return pair, nil
}


// PrivateKeyPEM returns the private key of the CAKeyPair PEM encoded
func (pair *CAKeyPair) PrivateKeyPEM() []byte {
	return pem.EncodeToMemory(
		&pem.Block{
			Type:  "BEGIN PRIVATE KEY",
			Bytes: x509.MarshalPKCS1PrivateKey(pair.PrivateKey),
		},
	)
}

// PrivateKeyPEM returns the CA cert of the CAKeyPair PEM encoded
func (pair *CAKeyPair) CACertPEM() []byte {
	return pem.EncodeToMemory(
		&pem.Block{
			Type:  "CERTIFICATE",
			Bytes: pair.Certificate,
		},
	)
}
