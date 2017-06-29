package puppy

/*
Copyright (c) 2012 Elazar Leibovich. All rights reserved.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are
met:

   * Redistributions of source code must retain the above copyright
notice, this list of conditions and the following disclaimer.
   * Redistributions in binary form must reproduce the above
copyright notice, this list of conditions and the following disclaimer
in the documentation and/or other materials provided with the
distribution.
   * Neither the name of Elazar Leibovich. nor the names of its
contributors may be used to endorse or promote products derived from
this software without specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
"AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT
OWNER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT
LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
(INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
*/

/*
Signer code used here was taken from:
https://github.com/elazarl/goproxy
*/

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"runtime"
	"sort"
	"time"
)

/*
counterecryptor.go
*/

type counterEncryptorRand struct {
	cipher  cipher.Block
	counter []byte
	rand    []byte
	ix      int
}

func newCounterEncryptorRandFromKey(key interface{}, seed []byte) (r counterEncryptorRand, err error) {
	var keyBytes []byte
	switch key := key.(type) {
	case *rsa.PrivateKey:
		keyBytes = x509.MarshalPKCS1PrivateKey(key)
	default:
		err = errors.New("only RSA keys supported")
		return
	}
	h := sha256.New()
	if r.cipher, err = aes.NewCipher(h.Sum(keyBytes)[:aes.BlockSize]); err != nil {
		return
	}
	r.counter = make([]byte, r.cipher.BlockSize())
	if seed != nil {
		copy(r.counter, h.Sum(seed)[:r.cipher.BlockSize()])
	}
	r.rand = make([]byte, r.cipher.BlockSize())
	r.ix = len(r.rand)
	return
}

func (c *counterEncryptorRand) Seed(b []byte) {
	if len(b) != len(c.counter) {
		panic("SetCounter: wrong counter size")
	}
	copy(c.counter, b)
}

func (c *counterEncryptorRand) refill() {
	c.cipher.Encrypt(c.rand, c.counter)
	for i := 0; i < len(c.counter); i++ {
		if c.counter[i]++; c.counter[i] != 0 {
			break
		}
	}
	c.ix = 0
}

func (c *counterEncryptorRand) Read(b []byte) (n int, err error) {
	if c.ix == len(c.rand) {
		c.refill()
	}
	if n = len(c.rand) - c.ix; n > len(b) {
		n = len(b)
	}
	copy(b, c.rand[c.ix:c.ix+n])
	c.ix += n
	return
}

/*
signer.go
*/

func hashSorted(lst []string) []byte {
	c := make([]string, len(lst))
	copy(c, lst)
	sort.Strings(c)
	h := sha1.New()
	for _, s := range c {
		h.Write([]byte(s + ","))
	}
	return h.Sum(nil)
}

func hashSortedBigInt(lst []string) *big.Int {
	rv := new(big.Int)
	rv.SetBytes(hashSorted(lst))
	return rv
}

var goproxySignerVersion = ":goroxy1"

func signHost(ca tls.Certificate, hosts []string) (cert tls.Certificate, err error) {
	var x509ca *x509.Certificate

	// Use the provided ca and not the global GoproxyCa for certificate generation.
	if x509ca, err = x509.ParseCertificate(ca.Certificate[0]); err != nil {
		return
	}
	start := time.Unix(0, 0)
	end, err := time.Parse("2006-01-02", "2049-12-31")
	if err != nil {
		panic(err)
	}
	hash := hashSorted(append(hosts, goproxySignerVersion, ":"+runtime.Version()))
	serial := new(big.Int)
	serial.SetBytes(hash)
	template := x509.Certificate{
		// TODO(elazar): instead of this ugly hack, just encode the certificate and hash the binary form.
		SerialNumber: serial,
		Issuer:       x509ca.Subject,
		Subject: pkix.Name{
			Organization: []string{"GoProxy untrusted MITM proxy Inc"},
		},
		NotBefore: start,
		NotAfter:  end,

		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			template.IPAddresses = append(template.IPAddresses, ip)
		} else {
			template.DNSNames = append(template.DNSNames, h)
		}
	}
	var csprng counterEncryptorRand
	if csprng, err = newCounterEncryptorRandFromKey(ca.PrivateKey, hash); err != nil {
		return
	}
	var certpriv *rsa.PrivateKey
	if certpriv, err = rsa.GenerateKey(&csprng, 1024); err != nil {
		return
	}
	var derBytes []byte
	if derBytes, err = x509.CreateCertificate(&csprng, &template, x509ca, &certpriv.PublicKey, ca.PrivateKey); err != nil {
		return
	}
	return tls.Certificate{
		Certificate: [][]byte{derBytes, ca.Certificate[0]},
		PrivateKey:  certpriv,
	}, nil
}
