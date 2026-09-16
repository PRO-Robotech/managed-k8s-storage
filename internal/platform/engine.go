package platform

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"github.com/PRO-Robotech/managed-k8s-storage/internal/model"
	"github.com/PRO-Robotech/managed-k8s-storage/internal/provider"
	"github.com/PRO-Robotech/managed-k8s-storage/internal/store"
	bolt "go.etcd.io/bbolt"
	"slices"
	"time"
)

type State struct {
	Volumes     map[string]*model.Volume
	Attachments map[string]*model.Attachment
}
type Engine struct {
	DB       *store.DB
	Provider provider.Provider
	Config   model.Config
}

func state(tx *bolt.Tx) (*State, error) {
	s := &State{Volumes: map[string]*model.Volume{}, Attachments: map[string]*model.Attachment{}}
	err := store.Read(tx, "platform", s)
	return s, err
}
func newID(prefix string) string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return prefix + hex.EncodeToString(b)
}
func owned(p model.Principal, v *model.Volume) bool {
	return v != nil && v.TenantID == p.TenantID && v.ClusterID == p.ClusterID
}
func (e *Engine) instance(p model.Principal, id string) (model.Instance, error) {
	i, ok := e.Config.Instances[id]
	if !ok || !i.Active || i.TenantID != p.TenantID || i.ClusterID != p.ClusterID {
		return i, model.Err("PermissionDenied", "instance outside active principal scope")
	}
	return i, nil
}
func active(s *State, volume string) bool {
	for _, a := range s.Attachments {
		if a.VolumeID == volume && a.State != "DETACHED" {
			return true
		}
	}
	return false
}
func (e *Engine) Call(ctx context.Context, p model.Principal, op string, r model.Request) (out model.Response, err error) {
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	var rejected error
	err = e.DB.Update(func(tx *bolt.Tx) error {
		s, x := state(tx)
		if x != nil {
			return x
		}
		out, rejected = e.apply(p, op, r, s)
		code := "OK"
		reason := ""
		if rejected != nil {
			code = "ERROR"
			reason = rejected.Error()
			if me, ok := rejected.(*model.Error); ok {
				code = me.Code
			}
		}
		// Do not log credentials or arbitrary context maps. IDs live only in audit.
		if x = store.Audit(tx, map[string]any{"time": time.Now().UTC(), "principal": p, "operation": op, "result": code, "reason": reason, "response": out, "name": r.Name, "volumeID": r.VolumeID, "id": r.ID, "attachmentID": r.AttachmentID, "generation": r.Generation, "podUID": r.PodUID, "podName": r.PodName, "namespace": r.Namespace, "serviceAccount": r.ServiceAccount}); x != nil {
			return x
		}
		if rejected != nil {
			return nil
		}
		return store.Write(tx, "platform", s)
	})
	if err == nil {
		err = rejected
	}
	return
}
func (e *Engine) apply(p model.Principal, op string, r model.Request, s *State) (out model.Response, err error) {
	if p.Role == "node" {
		return e.node(p, op, r, s)
	}
	if p.Role != "controller" {
		return out, model.Err("PermissionDenied", "controller identity required")
	}
	switch op {
	case "GetInstance":
		i, x := e.instance(p, r.InstanceID)
		out.Instance = &i
		return out, x
	case "CreateVolume":
		if !model.ValidID(r.Name) || r.SizeBytes <= 0 || r.SizeBytes > 1<<50 || r.Type != "standard" || !slices.Contains(e.Config.Zones[r.Region], r.Zone) {
			return out, model.Err("InvalidArgument", "invalid name, capacity, type or topology")
		}
		if (r.Mode != "mount" && r.Mode != "block") || (r.Mode == "mount" && r.FSType != "ext4" && r.FSType != "xfs") || (r.Mode == "block" && r.FSType != "") {
			return out, model.Err("InvalidArgument", "unsupported volume capability")
		}
		for _, v := range s.Volumes {
			if owned(p, v) && v.Name == r.Name {
				if v.DeleteRequested || v.InitialSizeBytes != r.SizeBytes || v.Type != r.Type || v.Region != r.Region || v.Zone != r.Zone || v.Mode != r.Mode || v.FSType != r.FSType {
					return out, model.Err("AlreadyExists", "idempotency key reused with different configuration or deleted volume")
				}
				out.Volume = v
				return out, nil
			}
		}
		v := &model.Volume{ID: newID("volume-"), TenantID: p.TenantID, ClusterID: p.ClusterID, Name: r.Name, Type: r.Type, Region: r.Region, Zone: r.Zone, InitialSizeBytes: r.SizeBytes, DesiredSizeBytes: r.SizeBytes, Mode: r.Mode, FSType: r.FSType, AllowFormat: true, State: "CREATING"}
		s.Volumes[v.ID] = v
		out.Volume = v
	case "GetVolume", "DeleteVolume", "ResizeVolume":
		v := s.Volumes[r.ID]
		if v == nil {
			if op == "DeleteVolume" {
				return out, nil
			}
			return out, model.Err("NotFound", "volume absent")
		}
		if !owned(p, v) {
			return out, model.Err("PermissionDenied", "volume outside principal scope")
		}
		out.Volume = v
		if op == "DeleteVolume" {
			if active(s, v.ID) {
				return out, model.Err("FailedPrecondition", "volume has live attachment")
			}
			v.DeleteRequested = true
			if v.State != "DELETED" {
				v.State = "DELETING"
			}
		}
		if op == "ResizeVolume" {
			if v.DeleteRequested || v.State == "CREATING" || r.SizeBytes < v.DesiredSizeBytes || r.SizeBytes > 1<<50 {
				return out, model.Err("FailedPrecondition", "resize requires available volume and cannot shrink")
			}
			v.DesiredSizeBytes = r.SizeBytes
			if v.SizeBytes < r.SizeBytes {
				v.State = "RESIZING"
			}
		}
	case "CreateAttachment":
		v := s.Volumes[r.VolumeID]
		if !owned(p, v) {
			return out, model.Err("PermissionDenied", "volume outside principal scope")
		}
		i, x := e.instance(p, r.InstanceID)
		if x != nil {
			return out, x
		}
		if i.Region != v.Region || i.Zone != v.Zone {
			return out, model.Err("FailedPrecondition", "topology mismatch")
		}
		if v.DeleteRequested || v.State != "AVAILABLE" || v.LastError != "" {
			return out, model.Err("FailedPrecondition", "volume not available")
		}
		for _, a := range s.Attachments {
			if a.VolumeID == v.ID && a.State != "DETACHED" {
				if a.InstanceID != i.ID || a.DeleteRequested {
					return out, model.Err("FailedPrecondition", "single writer attachment already exists or is draining")
				}
				out.Attachment = a
				return out, nil
			}
		}
		v.Generation++
		a := &model.Attachment{ID: newID("attachment-"), TenantID: v.TenantID, ClusterID: v.ClusterID, VolumeID: v.ID, InstanceID: i.ID, Generation: v.Generation, State: "ATTACHING", Uses: map[string]bool{}}
		s.Attachments[a.ID] = a
		out.Attachment = a
	case "GetAttachment", "DeleteAttachment":
		// Lookup by volume + instance supports CSI Unpublish even after a lost Publish response.
		a := s.Attachments[r.ID]
		if r.ID == "" {
			for _, candidate := range s.Attachments {
				if candidate.VolumeID == r.VolumeID && (r.InstanceID == "" || candidate.InstanceID == r.InstanceID) && (a == nil || candidate.Generation > a.Generation) {
					a = candidate
				}
			}
		}
		if a == nil {
			if op == "DeleteAttachment" {
				return out, nil
			}
			return out, model.Err("NotFound", "attachment absent")
		}
		if a.TenantID != p.TenantID || a.ClusterID != p.ClusterID {
			return out, model.Err("PermissionDenied", "attachment outside principal scope")
		}
		out.Attachment = a
		if op == "DeleteAttachment" && a.State != "DETACHED" {
			a.DeleteRequested = true
			a.State = "DETACHING"
		}
	default:
		return out, model.Err("Unimplemented", "unknown operation")
	}
	return
}
func (e *Engine) node(p model.Principal, op string, r model.Request, s *State) (out model.Response, err error) {
	i, x := e.instance(p, p.InstanceID)
	if x != nil {
		return out, x
	}
	if op == "NodeSelf" {
		out.Instance = &i
		return out, nil
	}
	if op != "AcquireUse" && op != "CheckUse" && op != "ReleaseUse" && op != "RecordPublish" && op != "MarkInitialized" {
		return out, model.Err("PermissionDenied", "node identity cannot manage cloud resources")
	}
	a := s.Attachments[r.AttachmentID]
	if a == nil {
		return out, model.Err("PermissionDenied", "mount requested without known attachment")
	}
	if a.TenantID != i.TenantID || a.ClusterID != i.ClusterID {
		return out, model.Err("PermissionDenied", "attachment tenant or cluster mismatch")
	}
	if a.VolumeID != r.VolumeID {
		return out, model.Err("PermissionDenied", "attachment volume mismatch")
	}
	if a.InstanceID != i.ID {
		return out, model.Err("PermissionDenied", "attachment instance mismatch")
	}
	if a.Generation != r.Generation {
		return out, model.Err("PermissionDenied", "invalid attachment generation")
	}
	if !model.ValidID(r.UseID) {
		return out, model.Err("InvalidArgument", "useID required")
	}
	v := s.Volumes[a.VolumeID]
	out.Attachment = a
	out.Volume = v
	if op == "ReleaseUse" {
		delete(a.Uses, r.UseID)
		return out, nil
	}
	if a.State != "ATTACHED" || a.DeleteRequested || a.LastError != "" || v == nil || v.LastError != "" || v.DeleteRequested {
		return out, model.Err("FailedPrecondition", "active healthy attachment required")
	}
	if op == "AcquireUse" {
		if r.Mode != v.Mode || r.FSType != v.FSType {
			return out, model.Err("InvalidArgument", "volume capability mismatch")
		}
		a.Uses[r.UseID] = true
		return out, nil
	}
	if !a.Uses[r.UseID] {
		return out, model.Err("FailedPrecondition", "durable use absent")
	}
	if op == "MarkInitialized" {
		v.AllowFormat = false
	}
	if op == "RecordPublish" && !model.ValidID(r.PodUID) {
		return out, model.Err("InvalidArgument", "Pod UID required")
	}
	return out, nil
}

// Reconcile is deliberately serialized with intent updates in this prototype.
// Durable IDs precede all provider effects; a failed transaction can be replayed.
func (e *Engine) Reconcile(ctx context.Context) error {
	return e.DB.Update(func(tx *bolt.Tx) error {
		s, err := state(tx)
		if err != nil {
			return err
		}
		for _, v := range s.Volumes {
			if v.State == "DELETED" {
				continue
			}
			v.LastError = ""
			if v.DeleteRequested {
				if active(s, v.ID) {
					continue
				}
				err = e.Provider.DeleteVolume(ctx, v.ID)
				if err == nil {
					v.State = "DELETED"
				}
			} else {
				observed, observeErr := e.Provider.GetVolume(ctx, v.ID)
				if observeErr != nil {
					v.LastError = observeErr.Error()
					continue
				}
				if observed == nil && v.State != "CREATING" {
					v.LastError = "previously created disk missing; never recreate data implicitly"
					continue
				}
				var d provider.Disk
				if observed != nil && observed.SizeBytes >= v.DesiredSizeBytes {
					d = *observed
					err = nil
				} else {
					d, err = e.Provider.EnsureVolume(ctx, *v)
				}
				if err == nil {
					if v.DeviceKey != "" && v.DeviceKey != d.DeviceKey {
						v.LastError = "device identity changed; recovery required"
						continue
					}
					v.SizeBytes = d.SizeBytes
					v.DeviceKey = d.DeviceKey
					v.State = "AVAILABLE"
				}
			}
			if err != nil {
				v.LastError = err.Error()
			}
		}
		for _, a := range s.Attachments {
			if a.State == "DETACHED" {
				continue
			}
			a.LastError = ""
			if a.DeleteRequested {
				if len(a.Uses) > 0 {
					continue
				}
				err = e.Provider.EnsureDetached(ctx, a.VolumeID, a.InstanceID, a.ID)
				if err == nil {
					a.State = "DETACHED"
				}
			} else {
				instance, exists := e.Config.Instances[a.InstanceID]
				if !exists || !instance.Active {
					a.LastError = "instance inactive: fencing required"
					continue
				}
				d, x := e.Provider.GetVolume(ctx, a.VolumeID)
				if x != nil {
					a.LastError = x.Error()
					continue
				}
				if a.State == "ATTACHED" && len(a.Uses) > 0 && (d == nil || d.InstanceID != a.InstanceID || d.AttachmentID != a.ID) {
					a.LastError = "cloud state drift with active use: fencing required"
					continue
				}
				if s.Volumes[a.VolumeID].LastError != "" {
					continue
				}
				err = e.Provider.EnsureAttached(ctx, a.VolumeID, a.InstanceID, a.ID)
				if err == nil {
					a.State = "ATTACHED"
				}
			}
			if err != nil {
				a.LastError = fmt.Sprint(err)
			}
		}
		return store.Write(tx, "platform", s)
	})
}
