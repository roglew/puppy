package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"crypto/sha1"
	"encoding/pem"
	"fmt"
	"math/big"
	"time"
)

type CAKeyPair struct {
	Certificate []byte
	PrivateKey *rsa.PrivateKey
}

func bigIntHash(n *big.Int) []byte {
	h := sha1.New()
	h.Write(n.Bytes())
	return h.Sum(nil)
}

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
			CommonName: "Puppy Proxy",
			Organization: []string{"Puppy Proxy"},
		},
		NotBefore: time.Now().Add(-5 * time.Minute).UTC(),
		NotAfter:  end,

		SubjectKeyId: bigIntHash(key.N),
		KeyUsage: x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA: true,
		MaxPathLenZero: true,
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("error generating certificate: %s", err.Error())
	}

	return &CAKeyPair{
		Certificate: derBytes,
		PrivateKey: key,
	}, nil
}

func (pair *CAKeyPair) PrivateKeyPEM() ([]byte) {
	return pem.EncodeToMemory(
		&pem.Block{
			Type: "BEGIN PRIVATE KEY",
			Bytes: x509.MarshalPKCS1PrivateKey(pair.PrivateKey),
		},
	)
}

func (pair *CAKeyPair) CACertPEM() ([]byte) {
	return pem.EncodeToMemory(
		&pem.Block{
			Type: "CERTIFICATE",
			Bytes: pair.Certificate,
		},
	)
}
