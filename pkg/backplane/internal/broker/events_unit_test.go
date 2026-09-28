package broker_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/gopherex/backplane/pkg/backplane/internal/broker"
	"github.com/gopherex/backplane/pkg/backplane/internal/env"
	"github.com/gopherex/backplane/pkg/backplane/internal/testlog"
)

// selfSigned is a PEM certificate and its PEM key.
func selfSigned(t *testing.T) (string, string) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "nats"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}

	der2, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}

	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der2}))
}

func TestTLSConfig(t *testing.T) {
	t.Parallel()

	cert, key := selfSigned(t)

	off, err := broker.TLSConfig(broker.TLS{CA: cert})
	if err != nil || off != nil {
		t.Fatalf("disabled: %v %v", off, err)
	}

	sys, err := broker.TLSConfig(broker.TLS{Enabled: true})
	if err != nil || sys.RootCAs != nil || len(sys.Certificates) != 0 || sys.MinVersion != tls.VersionTLS12 {
		t.Fatalf("system pool: %+v %v", sys, err)
	}

	full, err := broker.TLSConfig(broker.TLS{
		Enabled: true, CA: cert, Cert: cert, Key: key, ServerName: "nats.internal", InsecureSkipVerify: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	if full.RootCAs == nil || len(full.Certificates) != 1 || full.ServerName != "nats.internal" || !full.InsecureSkipVerify {
		t.Fatalf("full: %+v", full)
	}

	for name, c := range map[string]struct {
		tls  broker.TLS
		want error
	}{
		"bad ca":      {broker.TLS{Enabled: true, CA: "not a pem"}, broker.ErrTLSCA},
		"cert alone":  {broker.TLS{Enabled: true, Cert: cert}, broker.ErrTLSPair},
		"key alone":   {broker.TLS{Enabled: true, Key: key}, broker.ErrTLSPair},
		"bad keypair": {broker.TLS{Enabled: true, Cert: cert, Key: "junk"}, nil},
	} {
		_, err := broker.TLSConfig(c.tls)
		if err == nil || c.want != nil && !errors.Is(err, c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// A bad TLS configuration fails Connect before dialing.
func TestConnectRejectsBadTLS(t *testing.T) {
	t.Parallel()

	b := broker.New(broker.Params{
		URL: "nats://127.0.0.1:1", Service: "svc", Log: testlog.Discard(),
		TLS: broker.TLS{Enabled: true, CA: "junk"},
	})
	if err := b.Connect(t.Context(), newGroup(t)); !errors.Is(err, broker.ErrTLSCA) {
		t.Fatalf("want ErrTLSCA, got %v", err)
	}
}

func TestPublishTimeoutParam(t *testing.T) {
	t.Parallel()

	if d := broker.New(broker.Params{Service: "svc"}).PublishTimeout(); d != 5*time.Second {
		t.Fatalf("default %v", d)
	}

	if d := broker.New(broker.Params{Service: "svc", PublishTimeout: time.Second}).PublishTimeout(); d != time.Second {
		t.Fatalf("set %v", d)
	}
}

func TestNotConnected(t *testing.T) {
	t.Parallel()

	b := broker.New(broker.Params{Service: "svc"})
	if b.Connected() {
		t.Fatal("connected before Connect")
	}

	if _, err := b.JetStream(); !errors.Is(err, env.ErrUnavailable) {
		t.Fatalf("jetstream before Connect: %v", err)
	}
}

func TestHeadersExtensionsAndIncoming(t *testing.T) {
	t.Parallel()

	published := time.Date(2026, 1, 2, 3, 4, 5, 6, time.UTC)
	h := broker.BuildHeaders(context.Background(), broker.Event{
		ID: "id-1", Service: "iam", Instance: "iam-1", Version: "1.2.3", Type: "iam.UserRegistered",
		Key: "user-1", Time: published, Extensions: map[string]string{"tenant": "t1", "id": "spoofed"},
	})

	if h.Get("ce-tenant") != "t1" || h.Get("ce-id") != "id-1" || h.Get("Nats-Msg-Id") != "id-1" {
		t.Fatalf("headers %v", h)
	}

	// Header names may arrive canonicalized.
	canonical := nats.Header{}
	for k, v := range h {
		canonical[k] = v
	}

	canonical["Ce-Region"] = []string{"eu"}

	in := broker.Incoming(canonical, "mailer:iam.UserRegistered", 3)
	if in.ID != "id-1" || in.Source != "iam" || in.Type != "iam.UserRegistered" || in.Subject != "user-1" ||
		!in.Time.Equal(published) || in.Attempt != 3 || in.Consumer != "mailer:iam.UserRegistered" {
		t.Fatalf("incoming %+v", in)
	}

	want := map[string]string{"tenant": "t1", "region": "eu", "instance": "iam-1", "version": "1.2.3"}
	if len(in.Extensions) != len(want) {
		t.Fatalf("extensions %v", in.Extensions)
	}

	for k, v := range want {
		if in.Extensions[k] != v {
			t.Fatalf("extensions %v, want %v", in.Extensions, want)
		}
	}
}

func TestReactorSettingsOrderedInactive(t *testing.T) {
	t.Parallel()

	b := broker.New(broker.Params{Service: "mail", Log: testlog.Discard()})
	h := func(context.Context, []byte) ([]byte, error) { return nil, nil }

	got, err := b.ReactorSettings(env.Reactor{Event: "iam.UserRegistered", Consumer: "c", Handler: h, Delivery: env.Delivery{
		Ordered: true, InactiveThreshold: time.Hour,
	}})
	if err != nil {
		t.Fatal(err)
	}

	if got.Concurrency != 1 || got.Consumer.MaxAckPending != 1 || got.Consumer.InactiveThreshold != time.Hour {
		t.Fatalf("ordered: %+v", got)
	}
}
