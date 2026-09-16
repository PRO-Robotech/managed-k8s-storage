package transport

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/PRO-Robotech/managed-k8s-storage/internal/model"
	"github.com/PRO-Robotech/managed-k8s-storage/internal/platform"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

type Client struct {
	URL  string
	HTTP *http.Client
}
type Caller interface {
	Call(context.Context, string, model.Request) (model.Response, error)
}

func TLS(ca, cert, key string, server bool) (*tls.Config, error) {
	pem, err := os.ReadFile(ca)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("invalid CA")
	}
	pair, err := tls.LoadX509KeyPair(cert, key)
	if err != nil {
		return nil, err
	}
	c := &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{pair}, RootCAs: pool}
	if server {
		c.ClientCAs = pool
		c.ClientAuth = tls.RequireAndVerifyClientCert
	}
	return c, nil
}
func NewClient(url, ca, cert, key string) (*Client, error) {
	if !strings.HasPrefix(url, "https://") {
		return nil, fmt.Errorf("HTTPS required")
	}
	tc, err := TLS(ca, cert, key, false)
	if err != nil {
		return nil, err
	}
	return &Client{URL: strings.TrimRight(url, "/"), HTTP: &http.Client{Transport: &http.Transport{TLSClientConfig: tc}, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *Client) Call(ctx context.Context, op string, r model.Request) (out model.Response, err error) {
	body, err := json.Marshal(r)
	if err != nil {
		return out, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.URL+"/v1/"+op, bytes.NewReader(body))
	if err != nil {
		return out, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := c.HTTP.Do(req)
	if err != nil {
		return out, model.Err("Unavailable", "storage API unavailable")
	}
	defer res.Body.Close()
	dec := json.NewDecoder(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		var e model.Error
		if err = dec.Decode(&e); err != nil {
			return out, model.Err("Unavailable", "invalid API error response")
		}
		return out, &e
	}
	err = dec.Decode(&out)
	return
}
func Handler(e *platform.Engine) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		reject := func(err error) {
			var me *model.Error
			if !errors.As(err, &me) {
				me = &model.Error{Code: "Internal", Message: "storage operation failed"}
			}
			code := http.StatusBadRequest
			switch me.Code {
			case "PermissionDenied":
				code = 403
			case "Unauthenticated":
				code = 401
			case "NotFound":
				code = 404
			case "AlreadyExists", "FailedPrecondition":
				code = 409
			case "Internal":
				code = 500
			}
			w.WriteHeader(code)
			_ = json.NewEncoder(w).Encode(me)
		}
		if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.PeerCertificates) == 0 {
			reject(model.Err("Unauthenticated", "machine certificate required"))
			return
		}
		cert := r.TLS.PeerCertificates[0]
		if len(cert.URIs) != 1 {
			reject(model.Err("Unauthenticated", "exactly one registered URI SAN required"))
			return
		}
		principal, ok := e.Config.Principals[cert.URIs[0].String()]
		if !ok {
			reject(model.Err("PermissionDenied", "unregistered principal"))
			return
		}
		if r.Method != "POST" || !strings.HasPrefix(r.URL.Path, "/v1/") {
			http.NotFound(w, r)
			return
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
		dec.DisallowUnknownFields()
		var request model.Request
		if err := dec.Decode(&request); err != nil {
			reject(model.Err("InvalidArgument", "invalid request body"))
			return
		}
		var extra any
		if dec.Decode(&extra) != io.EOF {
			reject(model.Err("InvalidArgument", "extra request data"))
			return
		}
		out, err := e.Call(r.Context(), principal, strings.TrimPrefix(r.URL.Path, "/v1/"), request)
		if err != nil {
			reject(err)
			return
		}
		_ = json.NewEncoder(w).Encode(out)
	})
}
