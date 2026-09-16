package main

import (
	"flag"
	"github.com/PRO-Robotech/managed-k8s-storage/internal/driver"
	"github.com/PRO-Robotech/managed-k8s-storage/internal/host"
	"github.com/PRO-Robotech/managed-k8s-storage/internal/model"
	"github.com/PRO-Robotech/managed-k8s-storage/internal/runtime"
	"github.com/PRO-Robotech/managed-k8s-storage/internal/store"
	"log"
	"os"
	"time"
)

func main() {
	f := runtime.AddClientFlags()
	socket := flag.String("socket", "/var/lib/kubelet/plugins/storage.example.cloud/csi.sock", "absolute CSI socket path")
	root := flag.String("kubelet-root", "/var/lib/kubelet", "allowed CSI path root")
	journal := flag.String("journal", "/var/lib/platform-csi/node.db", "durable node journal")
	backend := flag.String("host-mode", "simulated", "simulated or linux (requires explicit root opt-in)")
	simfile := flag.String("host-db", "/var/lib/platform-csi/host.db", "simulated mount state")
	devices := flag.String("device-root", "/dev/disk/by-id", "trusted stable device links")
	flag.Parse()
	ctx, cancel := runtime.Context()
	defer cancel()
	api := f.Client()
	var self model.Response
	var err error
	for attempt := 0; attempt < 50; attempt++ {
		self, err = api.Call(ctx, "NodeSelf", model.Request{})
		if err == nil {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
	if err != nil {
		log.Fatal(err)
	}
	db, err := store.Open(*journal)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	var h host.Host
	switch *backend {
	case "simulated":
		d, err := store.Open(*simfile)
		if err != nil {
			log.Fatal(err)
		}
		defer d.Close()
		h = &host.Simulated{DB: d}
	case "linux":
		if os.Geteuid() != 0 {
			log.Fatal("linux host mode requires root in an isolated worker VM")
		}
		h = &host.Linux{DeviceRoot: *devices}
	default:
		log.Fatal("unknown host-mode")
	}
	n := &driver.Node{API: api, Host: h, Journal: db, Root: *root, Instance: *self.Instance}
	log.Printf("CSI Node %s at %s; host-mode=%s", self.Instance.ID, *socket, *backend)
	if err = driver.Serve(ctx, *socket, nil, n); err != nil && ctx.Err() == nil {
		log.Fatal(err)
	}
}
