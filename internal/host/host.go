package host

import (
	"context"
	"fmt"
	"github.com/PRO-Robotech/managed-k8s-storage/internal/model"
	"github.com/PRO-Robotech/managed-k8s-storage/internal/store"
	bolt "go.etcd.io/bbolt"
)

type Stats struct{ Total, Available, Used, Inodes, InodesFree int64 }
type Host interface {
	Stage(context.Context, model.Volume, string) error
	Publish(context.Context, model.Volume, string, string, bool) error
	Unmount(context.Context, string) error
	Expand(context.Context, model.Volume, string) error
	Stats(context.Context, model.Volume, string) (Stats, error)
}
type Mount struct {
	VolumeID, Source, Mode, FSType string
	ReadOnly                       bool
	Size                           int64
}
type Simulated struct{ DB *store.DB }

func (h *Simulated) update(fn func(map[string]Mount) error) error {
	return h.DB.Update(func(tx *bolt.Tx) error {
		m := map[string]Mount{}
		if err := store.Read(tx, "mounts", &m); err != nil {
			return err
		}
		if err := fn(m); err != nil {
			return err
		}
		return store.Write(tx, "mounts", m)
	})
}
func (h *Simulated) Stage(ctx context.Context, v model.Volume, path string) error {
	return h.update(func(m map[string]Mount) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		wanted := Mount{VolumeID: v.ID, Source: v.DeviceKey, Mode: v.Mode, FSType: v.FSType, Size: v.SizeBytes}
		if old, ok := m[path]; ok {
			if old.VolumeID != v.ID || old.Mode != v.Mode || old.FSType != v.FSType {
				return fmt.Errorf("stage collision")
			}
			return nil
		}
		m[path] = wanted
		return nil
	})
}
func (h *Simulated) Publish(ctx context.Context, v model.Volume, stage, target string, ro bool) error {
	return h.update(func(m map[string]Mount) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if m[stage].VolumeID != v.ID {
			return fmt.Errorf("stage absent")
		}
		wanted := Mount{VolumeID: v.ID, Source: stage, Mode: v.Mode, FSType: v.FSType, ReadOnly: ro, Size: v.SizeBytes}
		if old, ok := m[target]; ok {
			if old.VolumeID != v.ID || old.Source != stage || old.Mode != v.Mode || old.ReadOnly != ro {
				return fmt.Errorf("target collision")
			}
			return nil
		}
		m[target] = wanted
		return nil
	})
}
func (h *Simulated) Unmount(ctx context.Context, path string) error {
	return h.update(func(m map[string]Mount) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		delete(m, path)
		return nil
	})
}
func (h *Simulated) Expand(ctx context.Context, v model.Volume, path string) error {
	return h.update(func(m map[string]Mount) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		mount, ok := m[path]
		if !ok || mount.VolumeID != v.ID {
			return fmt.Errorf("mount absent")
		}
		for key, entry := range m {
			if entry.VolumeID == v.ID {
				entry.Size = v.SizeBytes
				m[key] = entry
			}
		}
		return nil
	})
}
func (h *Simulated) Stats(ctx context.Context, v model.Volume, path string) (out Stats, err error) {
	err = h.update(func(m map[string]Mount) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		mount, ok := m[path]
		if !ok || mount.VolumeID != v.ID {
			return fmt.Errorf("mount absent")
		}
		out = Stats{Total: mount.Size, Available: mount.Size, Inodes: 1000, InodesFree: 1000}
		return nil
	})
	return
}
