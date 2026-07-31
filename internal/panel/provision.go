package panel

import (
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"net"
	"time"

	"github.com/singpanel/singpanel/internal/singbox"
)

// genRealityKeypair generates an X25519 keypair in the exact format the
// official `sing-box generate reality-keypair` produces (raw base64url, no
// padding), so no sing-box binary is needed on the panel host.
func genRealityKeypair() (privateKey, publicKey string, err error) {
	c := ecdh.X25519()
	k, err := c.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	privateKey = base64.RawURLEncoding.EncodeToString(k.Bytes())
	publicKey = base64.RawURLEncoding.EncodeToString(k.PublicKey().Bytes())
	return privateKey, publicKey, nil
}

// genSSPSK returns a base64 server PSK of the correct length for a Shadowsocks
// 2022 method (16 or 32 bytes), or a 32-byte random string for legacy methods.
func genSSPSK(method string) string {
	n := singbox.SS2022KeyLen(method)
	if n == 0 {
		n = 16
	}
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.StdEncoding.EncodeToString(b)
}

// fillInboundSecrets auto-populates missing secrets on an inbound's settings so
// admins can create protocols without hand-generating keys:
//   - REALITY: generate the X25519 keypair + a short_id when absent
//   - Shadowsocks: generate the server PSK when absent
func fillInboundSecrets(typ string, s *singbox.InboundSettings) error {
	// Panel-managed inbounds are always single-credential: one fixed credential
	// per protocol, never a per-panel-user list. Which wire shape that takes is
	// protocol-specific (users[] with a single entry vs a top-level key) and is
	// handled by BuildInbound.
	s.SingleUser = true
	if s.TLS.Reality.Enabled && s.TLS.Reality.PrivateKey == "" {
		priv, pub, err := genRealityKeypair()
		if err != nil {
			return err
		}
		s.TLS.Reality.PrivateKey = priv
		s.TLS.Reality.PublicKey = pub
		if len(s.TLS.Reality.ShortID) == 0 {
			s.TLS.Reality.ShortID = []string{randHex(4)} // 8 hex chars
		}
		if s.TLS.Reality.HandshakeServer == "" {
			s.TLS.Reality.HandshakeServer = "www.microsoft.com"
		}
		if s.TLS.Reality.HandshakeServerPort == 0 {
			s.TLS.Reality.HandshakeServerPort = 443
		}
	}
	// Self-signed TLS: issue the certificate once and keep the PEM on the inbound
	// (regenerating on every push would change the cert under live clients).
	if s.TLS.SelfSigned && (s.TLS.Certificate == "" || s.TLS.Key == "") {
		cert, key, err := genSelfSignedCert(s.TLS.ServerName)
		if err != nil {
			return err
		}
		s.TLS.Enabled = true
		s.TLS.Certificate = cert
		s.TLS.Key = key
		s.TLS.Insecure = true // clients must skip verification for a self-signed cert
	}
	if typ == "shadowsocks" && s.SSServerPSK == "" {
		if s.Method == "" {
			s.Method = "2022-blake3-aes-128-gcm"
		}
		s.SSServerPSK = genSSPSK(s.Method)
	}
	if typ == "snell" {
		if s.SnellVersion == 0 {
			s.SnellVersion = 5
		}
		if s.SnellPSK == "" {
			s.SnellPSK = randHex(16)
		}
	}
	if typ == "shadowtls" {
		if s.ShadowTLSVersion == 0 {
			s.ShadowTLSVersion = 3
		}
		if s.ShadowTLSHandshake == "" {
			s.ShadowTLSHandshake = "www.microsoft.com"
		}
		if s.ShadowTLSHandshakePort == 0 {
			s.ShadowTLSHandshakePort = 443
		}
		if s.ShadowTLSVersion == 2 && s.ShadowTLSPassword == "" {
			s.ShadowTLSPassword = randHex(16)
		}
	}
	// Single-credential inbounds carry their own fixed identity. Fill whatever is
	// still blank so the admin never has to hand-generate a key (they can still
	// set/regenerate it in the form). The credential kind is protocol-specific:
	// UUID for vless/vmess/tuic, a password for the rest (SS/snell use their own
	// PSK fields, populated above).
	if s.SingleUser {
		switch typ {
		case "vless", "vmess", "tuic":
			if s.UUID == "" {
				s.UUID = newUUID()
			}
		}
		switch typ {
		case "trojan", "hysteria2", "hysteria", "naive", "anytls", "tuic", "shadowtls":
			if s.Password == "" {
				s.Password = randHex(16)
			}
		}
	}
	return nil
}

// genSelfSignedCert issues a long-lived self-signed certificate for host,
// equivalent to the usual
//
//	openssl req -x509 -sha256 -nodes -days 3650 -newkey … -keyout key -out pem
//
// but done in-process, so the admin never has to SSH into a node to create one.
// The PEM is stored on the inbound and emitted inline into the config, so there
// is no file to manage or lose. ECDSA P-256 is used instead of RSA-4096: it is
// far cheaper to generate, every sing-box client supports it, and for a cert
// nobody validates anyway the key type is irrelevant.
func genSelfSignedCert(host string) (certPEM, keyPEM string, err error) {
	if host == "" {
		host = "localhost"
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return "", "", err
	}
	tmpl := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: host},
		NotBefore:             time.Now().Add(-1 * time.Hour), // tolerate node clock skew
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	if ip := net.ParseIP(host); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{host}
	}

	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return "", "", err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", "", err
	}
	certPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	keyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	return certPEM, keyPEM, nil
}
