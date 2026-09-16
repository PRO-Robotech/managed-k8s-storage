package main

import (
	"flag"
	"github.com/PRO-Robotech/managed-k8s-storage/internal/driver"
	"github.com/PRO-Robotech/managed-k8s-storage/internal/runtime"
	"log"
)

func main() {
	f := runtime.AddClientFlags()
	socket := flag.String("socket", "/run/platform-csi/controller.sock", "absolute CSI socket path")
	region := flag.String("region", "ru-1", "fallback region")
	zone := flag.String("zone", "ru-1a", "fallback zone")
	flag.Parse()
	ctx, cancel := runtime.Context()
	defer cancel()
	c := &driver.Controller{API: f.Client(), Region: *region, Zone: *zone}
	log.Printf("external CSI Controller at %s", *socket)
	if err := driver.Serve(ctx, *socket, c, nil); err != nil && ctx.Err() == nil {
		log.Fatal(err)
	}
}
