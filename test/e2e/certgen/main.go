// Command certgen issues the E2E test PKI (#27, #29): one throw-away root
// CA and a server certificate for localhost that all three example proxies
// (Caddy, Traefik, nginx) in e2e/compose.yaml serve, so the browser and
// Node trust every origin through one root (the e2e workflow job trusts
// ca.crt).
//
//	certgen -dir /certs            write ca.crt, localhost.crt, localhost.key
//	                               (kept if already present), then wait for
//	                               SIGTERM so `docker compose up --wait` sees
//	                               a healthy service
//	certgen -dir /certs -check     exit 0 when the files exist (healthcheck)
//	certgen -dir /certs -once      write the files and exit
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"math/big"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

// Output file names.
const (
	CAFile   = "ca.crt"
	CertFile = "localhost.crt"
	KeyFile  = "localhost.key"
)

func main() {
	dir := flag.String("dir", "/certs", "output directory")
	check := flag.Bool("check", false, "only check that the files exist")
	once := flag.Bool("once", false, "exit after writing")
	flag.Parse()
	if *check {
		for _, f := range []string{CAFile, CertFile, KeyFile} {
			if st, err := os.Stat(filepath.Join(*dir, f)); err != nil || st.Size() == 0 {
				fmt.Fprintln(os.Stderr, "missing", f)
				os.Exit(1)
			}
		}
		return
	}
	if err := Generate(*dir, time.Now()); err != nil {
		fmt.Fprintln(os.Stderr, "certgen:", err)
		os.Exit(1)
	}
	fmt.Fprintln(os.Stdout, "certgen: certificates ready in", *dir)
	if *once {
		return
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
}

// Generate writes the CA and localhost certificate into dir unless all
// three files already exist.
func Generate(dir string, now time.Time) error {
	if exists(dir, CAFile) && exists(dir, CertFile) && exists(dir, KeyFile) {
		return nil
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: "DockYard E2E Root CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(30 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		return err
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		return err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	leaf := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: "localhost"},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(7 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, caCert, &key.PublicKey, caKey)
	if err != nil {
		return err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	return errors.Join(
		write(dir, KeyFile, "PRIVATE KEY", keyDER, 0o644), // test-only key; proxies read it as root
		write(dir, CertFile, "CERTIFICATE", leafDER, 0o644),
		write(dir, CAFile, "CERTIFICATE", caDER, 0o644),
	)
}

func serial() *big.Int {
	n, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	return n
}

func exists(dir, name string) bool {
	st, err := os.Stat(filepath.Join(dir, name))
	return err == nil && st.Size() > 0
}

func write(dir, name, typ string, der []byte, mode os.FileMode) error {
	return os.WriteFile(filepath.Join(dir, name), pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}), mode)
}
