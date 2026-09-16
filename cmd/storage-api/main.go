package main

import (
	"encoding/json"
	"flag"
	"github.com/PRO-Robotech/managed-k8s-storage/internal/model"
	"github.com/PRO-Robotech/managed-k8s-storage/internal/platform"
	"github.com/PRO-Robotech/managed-k8s-storage/internal/provider"
	"github.com/PRO-Robotech/managed-k8s-storage/internal/runtime"
	"github.com/PRO-Robotech/managed-k8s-storage/internal/store"
	"github.com/PRO-Robotech/managed-k8s-storage/internal/transport"
	"log"
	"net/http"
	"os"
	"time"
)

func main() {
	addr := flag.String("listen", "127.0.0.1:9443", "HTTPS listen address")
	config := flag.String("config", "deploy/registry.example.json", "trusted registry file")
	dbfile := flag.String("db", "run/platform.db", "platform state")
	cloudfile := flag.String("cloud-db", "run/cloud.db", "emulated provider state")
	ca := flag.String("ca", "", "client CA PEM")
	cert := flag.String("cert", "", "server certificate PEM")
	key := flag.String("key", "", "server private key")
	flag.Parse()
	b, err := os.ReadFile(*config)
	must(err)
	var cfg model.Config
	must(json.Unmarshal(b, &cfg))
	if len(cfg.Principals) == 0 || len(cfg.Zones) == 0 {
		log.Fatal("registry must define principals and zones")
	}
	db, err := store.Open(*dbfile)
	must(err)
	defer db.Close()
	cloud, err := store.Open(*cloudfile)
	must(err)
	defer cloud.Close()
	engine := &platform.Engine{DB: db, Provider: &provider.Fake{DB: cloud}, Config: cfg}
	tc, err := transport.TLS(*ca, *cert, *key, true)
	must(err)
	ctx, cancel := runtime.Context()
	defer cancel()
	server := &http.Server{Addr: *addr, Handler: transport.Handler(engine), TLSConfig: tc, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: time.Minute}
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := engine.Reconcile(ctx); err != nil {
					log.Printf("reconcile: %v", err)
				}
			}
		}
	}()
	go func() { <-ctx.Done(); _ = server.Close() }()
	log.Printf("Storage API at %s; persistent EMULATED cloud provider", *addr)
	if err = server.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
