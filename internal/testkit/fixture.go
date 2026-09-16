// Package testkit supplies isolated, synthetic tenants and persistent fake state.
package testkit

import (
	"context"
	"github.com/PRO-Robotech/managed-k8s-storage/internal/model"
	"github.com/PRO-Robotech/managed-k8s-storage/internal/platform"
	"github.com/PRO-Robotech/managed-k8s-storage/internal/provider"
	"github.com/PRO-Robotech/managed-k8s-storage/internal/store"
	"path/filepath"
	"testing"
)

var ControllerA = model.Principal{Role: "controller", TenantID: "tenant-a", ClusterID: "cluster-a"}
var ControllerB = model.Principal{Role: "controller", TenantID: "tenant-b", ClusterID: "cluster-b"}
var NodeA = model.Principal{Role: "node", TenantID: "tenant-a", ClusterID: "cluster-a", InstanceID: "vm-a"}
var NodeA2 = model.Principal{Role: "node", TenantID: "tenant-a", ClusterID: "cluster-a", InstanceID: "vm-a2"}
var NodeB = model.Principal{Role: "node", TenantID: "tenant-b", ClusterID: "cluster-b", InstanceID: "vm-b"}

type Local struct {
	E         *platform.Engine
	Principal model.Principal
}

func (l Local) Call(ctx context.Context, op string, r model.Request) (model.Response, error) {
	return l.E.Call(ctx, l.Principal, op, r)
}
func New(t testing.TB) *platform.Engine {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	cloud, err := store.Open(filepath.Join(dir, "cloud.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(); cloud.Close() })
	cfg := model.Config{Instances: map[string]model.Instance{}, Principals: map[string]model.Principal{}, Zones: map[string][]string{"ru-1": {"ru-1a", "ru-1b"}}}
	for _, p := range []model.Principal{NodeA, NodeA2, NodeB} {
		cfg.Instances[p.InstanceID] = model.Instance{ID: p.InstanceID, TenantID: p.TenantID, ClusterID: p.ClusterID, Region: "ru-1", Zone: "ru-1a", Active: true}
	}
	cfg.Principals["spiffe://storage.example.cloud/node/vm-a"] = NodeA
	cfg.Principals["spiffe://storage.example.cloud/controller/cluster-a"] = ControllerA
	return &platform.Engine{DB: db, Provider: &provider.Fake{DB: cloud}, Config: cfg}
}
func CreateRequest(name string) model.Request {
	return model.Request{Name: name, Type: "standard", Region: "ru-1", Zone: "ru-1a", Mode: "mount", FSType: "ext4", SizeBytes: 1 << 30}
}
func Call(t testing.TB, e *platform.Engine, p model.Principal, op string, r model.Request) model.Response {
	t.Helper()
	out, err := e.Call(context.Background(), p, op, r)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func Reconcile(t testing.TB, e *platform.Engine) {
	t.Helper()
	if err := e.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func Volume(t testing.TB, e *platform.Engine, p model.Principal, name string) *model.Volume {
	t.Helper()
	v := Call(t, e, p, "CreateVolume", CreateRequest(name)).Volume
	Reconcile(t, e)
	return Call(t, e, p, "GetVolume", model.Request{ID: v.ID}).Volume
}
func Attached(t testing.TB, e *platform.Engine) (*model.Volume, *model.Attachment) {
	t.Helper()
	v := Volume(t, e, ControllerA, "pvc-a")
	a := Call(t, e, ControllerA, "CreateAttachment", model.Request{VolumeID: v.ID, InstanceID: "vm-a"}).Attachment
	Reconcile(t, e)
	a = Call(t, e, ControllerA, "GetAttachment", model.Request{ID: a.ID}).Attachment
	return v, a
}
func Proof(v *model.Volume, a *model.Attachment) model.Request {
	return model.Request{VolumeID: v.ID, AttachmentID: a.ID, Generation: a.Generation, UseID: "test-use", Mode: v.Mode, FSType: v.FSType}
}
