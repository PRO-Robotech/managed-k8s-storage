package runtime

import (
	"context"
	"flag"
	"github.com/PRO-Robotech/managed-k8s-storage/internal/transport"
	"log"
	"os"
	"os/signal"
	"syscall"
)

type ClientFlags struct{ URL, CA, Cert, Key *string }

func AddClientFlags() ClientFlags {
	return ClientFlags{flag.String("api", "https://localhost:9443", "Storage API URL"), flag.String("ca", "", "platform CA PEM"), flag.String("cert", "", "machine certificate PEM"), flag.String("key", "", "machine private key")}
}
func (f ClientFlags) Client() *transport.Client {
	c, err := transport.NewClient(*f.URL, *f.CA, *f.Cert, *f.Key)
	if err != nil {
		log.Fatal(err)
	}
	return c
}
func Context() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}
