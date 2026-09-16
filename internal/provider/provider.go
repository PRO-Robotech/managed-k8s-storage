// Package provider is the only place where a cloud-specific implementation belongs.
package provider

import (
	"context"
	"fmt"
	"github.com/PRO-Robotech/managed-k8s-storage/internal/model"
	"github.com/PRO-Robotech/managed-k8s-storage/internal/store"
	bolt "go.etcd.io/bbolt"
)

type Disk struct {
	ID, DeviceKey            string
	SizeBytes                int64
	InstanceID, AttachmentID string
}
type Provider interface {
	EnsureVolume(context.Context, model.Volume) (Disk, error)
	GetVolume(context.Context, string) (*Disk, error)
	DeleteVolume(context.Context, string) error
	EnsureAttached(context.Context, string, string, string) error
	EnsureDetached(context.Context, string, string, string) error
}

// Fake persists actual state independently of the platform DB so lost-response
// and restart tests exercise the same idempotency boundary as a remote provider.
type Fake struct{ DB *store.DB }

func (f *Fake) mutate(fn func(map[string]Disk) error) error {
	return f.DB.Update(func(tx *bolt.Tx) error {
		m := map[string]Disk{}
		if err := store.Read(tx, "disks", &m); err != nil {
			return err
		}
		if err := fn(m); err != nil {
			return err
		}
		return store.Write(tx, "disks", m)
	})
}
func (f *Fake) GetVolume(ctx context.Context, id string) (out *Disk, err error) {
	err = f.DB.View(func(tx *bolt.Tx) error {
		m := map[string]Disk{}
		if e := store.Read(tx, "disks", &m); e != nil {
			return e
		}
		if d, ok := m[id]; ok {
			out = &d
		}
		return ctx.Err()
	})
	return
}
func (f *Fake) EnsureVolume(ctx context.Context, v model.Volume) (out Disk, err error) {
	err = f.mutate(func(m map[string]Disk) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		d, ok := m[v.ID]
		if !ok {
			d = Disk{ID: v.ID, DeviceKey: "platform-" + v.ID}
		}
		if v.DesiredSizeBytes > d.SizeBytes {
			d.SizeBytes = v.DesiredSizeBytes
		}
		m[v.ID] = d
		out = d
		return nil
	})
	return
}
func (f *Fake) DeleteVolume(ctx context.Context, id string) error {
	return f.mutate(func(m map[string]Disk) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if m[id].InstanceID != "" {
			return fmt.Errorf("disk still attached")
		}
		delete(m, id)
		return nil
	})
}
func (f *Fake) EnsureAttached(ctx context.Context, id, vm, attachment string) error {
	return f.mutate(func(m map[string]Disk) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		d, ok := m[id]
		if !ok {
			return fmt.Errorf("disk missing")
		}
		if d.InstanceID != "" && (d.InstanceID != vm || d.AttachmentID != attachment) {
			return fmt.Errorf("cloud single writer conflict")
		}
		d.InstanceID = vm
		d.AttachmentID = attachment
		m[id] = d
		return nil
	})
}
func (f *Fake) EnsureDetached(ctx context.Context, id, vm, attachment string) error {
	return f.mutate(func(m map[string]Disk) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		d, ok := m[id]
		if !ok || d.InstanceID == "" {
			return nil
		}
		if d.InstanceID != vm || d.AttachmentID != attachment {
			return fmt.Errorf("cloud attachment identity mismatch")
		}
		d.InstanceID = ""
		d.AttachmentID = ""
		m[id] = d
		return nil
	})
}
