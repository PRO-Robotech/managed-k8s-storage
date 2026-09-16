package transport_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"github.com/PRO-Robotech/managed-k8s-storage/internal/model"
	"github.com/PRO-Robotech/managed-k8s-storage/internal/testkit"
	"github.com/PRO-Robotech/managed-k8s-storage/internal/transport"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestMachineMTLSBoundary(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test-ca"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, pub, key)
	if err != nil {
		t.Fatal(err)
	}
	root, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(root)
	issue := func(uri string) tls.Certificate {
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		u, err := url.Parse(uri)
		if err != nil {
			t.Fatal(err)
		}
		tpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: "controller/cluster-a"}, URIs: []*url.URL{u}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
		der, err := x509.CreateCertificate(rand.Reader, tpl, root, pub, key)
		if err != nil {
			t.Fatal(err)
		}
		return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv}
	}
	e := testkit.New(t)
	s := httptest.NewUnstartedServer(transport.Handler(e))
	s.TLS = &tls.Config{MinVersion: tls.VersionTLS13, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool}
	s.StartTLS()
	defer s.Close()
	serverPool := x509.NewCertPool()
	serverPool.AddCert(s.Certificate())
	client := func(cert *tls.Certificate) *transport.Client {
		cfg := &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: serverPool}
		if cert != nil {
			cfg.Certificates = []tls.Certificate{*cert}
		}
		tr := &http.Transport{TLSClientConfig: cfg}
		t.Cleanup(tr.CloseIdleConnections)
		return &transport.Client{URL: s.URL, HTTP: &http.Client{Transport: tr, Timeout: time.Second}}
	}
	controller := issue("spiffe://storage.example.cloud/controller/cluster-a")
	node := issue("spiffe://storage.example.cloud/node/vm-a")
	unknown := issue("spiffe://storage.example.cloud/controller/cluster-unknown")
	v, err := client(&controller).Call(context.Background(), "CreateVolume", testkit.CreateRequest("pvc-tls"))
	if err != nil || v.Volume == nil {
		t.Fatal("controller failed", err)
	}
	cases := []struct {
		name, op, want string
		c              *transport.Client
		r              model.Request
	}{
		{"no client cert", "NodeSelf", "Unavailable", client(nil), model.Request{}},
		{"CN cannot replace registered URI", "NodeSelf", "PermissionDenied", client(&unknown), model.Request{}},
		{"node cannot delete", "DeleteVolume", "PermissionDenied", client(&node), model.Request{ID: v.Volume.ID}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := c.c.Call(context.Background(), c.op, c.r)
			var me *model.Error
			if !errors.As(err, &me) || me.Code != c.want {
				t.Fatalf("want %s, got %v", c.want, err)
			}
		})
	}
	self, err := client(&node).Call(context.Background(), "NodeSelf", model.Request{InstanceID: "vm-b"})
	if err != nil || self.Instance.ID != "vm-a" {
		t.Fatal("client parameter overrode authenticated machine", err)
	}
}
