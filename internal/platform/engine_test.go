package platform_test

import (
	"context"
	"errors"
	"github.com/PRO-Robotech/managed-k8s-storage/internal/model"
	"github.com/PRO-Robotech/managed-k8s-storage/internal/platform"
	"github.com/PRO-Robotech/managed-k8s-storage/internal/provider"
	"github.com/PRO-Robotech/managed-k8s-storage/internal/store"
	"github.com/PRO-Robotech/managed-k8s-storage/internal/testkit"
	"sync"
	"testing"
)

var ctx = context.Background()

func denied(t *testing.T, err error, code string) {
	t.Helper()
	var e *model.Error
	if !errors.As(err, &e) || e.Code != code {
		t.Fatalf("want %s, got %v", code, err)
	}
}
func TestTenantAndNodeAuthorization(t *testing.T) {
	e := testkit.New(t)
	v, a := testkit.Attached(t, e)
	p := testkit.Proof(v, a)
	cases := []struct {
		name, op, code string
		p              model.Principal
		r              model.Request
	}{
		{"foreign read", "GetVolume", "PermissionDenied", testkit.ControllerB, model.Request{ID: v.ID}},
		{"foreign delete", "DeleteVolume", "PermissionDenied", testkit.ControllerB, model.Request{ID: v.ID}},
		{"foreign resize", "ResizeVolume", "PermissionDenied", testkit.ControllerB, model.Request{ID: v.ID, SizeBytes: 2 << 30}},
		{"foreign attach", "CreateAttachment", "PermissionDenied", testkit.ControllerB, model.Request{VolumeID: v.ID, InstanceID: "vm-b"}},
		{"forged node ID", "CreateAttachment", "PermissionDenied", testkit.ControllerA, model.Request{VolumeID: v.ID, InstanceID: "vm-b"}},
		{"foreign mount", "AcquireUse", "PermissionDenied", testkit.NodeB, p},
		{"wrong own VM", "AcquireUse", "PermissionDenied", testkit.NodeA2, p},
		{"node cloud mutation", "DeleteVolume", "PermissionDenied", testkit.NodeA, model.Request{ID: v.ID}},
		{"node enumeration", "GetVolume", "PermissionDenied", testkit.NodeA, model.Request{ID: v.ID}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { _, err := e.Call(ctx, c.p, c.op, c.r); denied(t, err, c.code) })
	}
	p.Generation++
	_, err := e.Call(ctx, testkit.NodeA, "AcquireUse", p)
	denied(t, err, "PermissionDenied")
}
func TestIdempotencyAndConcurrentSingleWriter(t *testing.T) {
	e := testkit.New(t)
	v := testkit.Volume(t, e, testkit.ControllerA, "pvc-a")
	again := testkit.Call(t, e, testkit.ControllerA, "CreateVolume", testkit.CreateRequest("pvc-a")).Volume
	if v.ID != again.ID {
		t.Fatal("duplicate volume")
	}
	different := testkit.CreateRequest("pvc-a")
	different.FSType = "xfs"
	_, err := e.Call(ctx, testkit.ControllerA, "CreateVolume", different)
	denied(t, err, "AlreadyExists")
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, vm := range []string{"vm-a", "vm-a2"} {
		wg.Add(1)
		go func(vm string) {
			defer wg.Done()
			_, err := e.Call(ctx, testkit.ControllerA, "CreateAttachment", model.Request{VolumeID: v.ID, InstanceID: vm})
			results <- err
		}(vm)
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else {
			denied(t, err, "FailedPrecondition")
		}
	}
	if success != 1 {
		t.Fatalf("successful writers %d", success)
	}
}
func TestDetachBarrierAndGeneration(t *testing.T) {
	e := testkit.New(t)
	v, a := testkit.Attached(t, e)
	p := testkit.Proof(v, a)
	testkit.Call(t, e, testkit.NodeA, "AcquireUse", p)
	testkit.Call(t, e, testkit.ControllerA, "DeleteAttachment", model.Request{ID: a.ID})
	testkit.Reconcile(t, e)
	d, err := e.Provider.GetVolume(ctx, v.ID)
	if err != nil || d.InstanceID != "vm-a" {
		t.Fatalf("detached mounted disk: %v %v", d, err)
	}
	_, err = e.Call(ctx, testkit.NodeA, "AcquireUse", p)
	denied(t, err, "FailedPrecondition")
	_, err = e.Call(ctx, testkit.ControllerA, "DeleteVolume", model.Request{ID: v.ID})
	denied(t, err, "FailedPrecondition")
	testkit.Call(t, e, testkit.NodeA, "ReleaseUse", p)
	testkit.Reconcile(t, e)
	b := testkit.Call(t, e, testkit.ControllerA, "CreateAttachment", model.Request{VolumeID: v.ID, InstanceID: "vm-a2"}).Attachment
	if b.Generation != a.Generation+1 || b.ID == a.ID {
		t.Fatal("attachment incarnation reused")
	}
	testkit.Reconcile(t, e)
	_, err = e.Call(ctx, testkit.NodeA, "AcquireUse", p)
	denied(t, err, "FailedPrecondition")
}
func TestCancelAttachBeforeCloudOperation(t *testing.T) {
	e := testkit.New(t)
	v := testkit.Volume(t, e, testkit.ControllerA, "pvc-a")
	a := testkit.Call(t, e, testkit.ControllerA, "CreateAttachment", model.Request{VolumeID: v.ID, InstanceID: "vm-a"}).Attachment
	testkit.Call(t, e, testkit.ControllerA, "DeleteAttachment", model.Request{ID: a.ID})
	testkit.Reconcile(t, e)
	d, err := e.Provider.GetVolume(ctx, v.ID)
	if err != nil || d.InstanceID != "" {
		t.Fatal("cancelled intent attached", err)
	}
}

type lostResponse struct {
	provider.Provider
	once bool
}

func (p *lostResponse) EnsureAttached(c context.Context, v, i, a string) error {
	if err := p.Provider.EnsureAttached(c, v, i, a); err != nil {
		return err
	}
	if !p.once {
		p.once = true
		return errors.New("cloud response lost after commit")
	}
	return nil
}
func TestCloudTimeoutAndPlatformRestart(t *testing.T) {
	e := testkit.New(t)
	v := testkit.Volume(t, e, testkit.ControllerA, "pvc-a")
	a := testkit.Call(t, e, testkit.ControllerA, "CreateAttachment", model.Request{VolumeID: v.ID, InstanceID: "vm-a"}).Attachment
	e.Provider = &lostResponse{Provider: e.Provider}
	testkit.Reconcile(t, e)
	disk, _ := e.Provider.GetVolume(ctx, v.ID)
	if disk.AttachmentID != a.ID {
		t.Fatal("fault was not after cloud commit")
	}
	before := testkit.Call(t, e, testkit.ControllerA, "GetAttachment", model.Request{ID: a.ID}).Attachment
	if before.State != "ATTACHING" || before.LastError == "" {
		t.Fatal("unknown outcome not retained")
	}
	file := e.DB.Path()
	if err := e.DB.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	restored := &platform.Engine{DB: db, Provider: e.Provider, Config: e.Config}
	testkit.Reconcile(t, restored)
	after := testkit.Call(t, restored, testkit.ControllerA, "CreateAttachment", model.Request{VolumeID: v.ID, InstanceID: "vm-a"}).Attachment
	if after.ID != a.ID || after.State != "ATTACHED" {
		t.Fatal("did not recover stable attachment")
	}
}
func TestCloudDriftNeverRecreatesMissingVolume(t *testing.T) {
	e := testkit.New(t)
	v := testkit.Volume(t, e, testkit.ControllerA, "pvc-a")
	if err := e.Provider.DeleteVolume(ctx, v.ID); err != nil {
		t.Fatal(err)
	}
	testkit.Reconcile(t, e)
	d, err := e.Provider.GetVolume(ctx, v.ID)
	if err != nil || d != nil {
		t.Fatal("missing disk silently recreated")
	}
	got := testkit.Call(t, e, testkit.ControllerA, "GetVolume", model.Request{ID: v.ID}).Volume
	if got.LastError == "" {
		t.Fatal("missing disk not surfaced")
	}
}
func TestCloudDetachedWithMountedUseFailsClosed(t *testing.T) {
	e := testkit.New(t)
	v, a := testkit.Attached(t, e)
	p := testkit.Proof(v, a)
	testkit.Call(t, e, testkit.NodeA, "AcquireUse", p)
	if err := e.Provider.EnsureDetached(ctx, v.ID, "vm-a", a.ID); err != nil {
		t.Fatal(err)
	}
	testkit.Reconcile(t, e)
	_, err := e.Call(ctx, testkit.NodeA, "CheckUse", p)
	denied(t, err, "FailedPrecondition")
	d, _ := e.Provider.GetVolume(ctx, v.ID)
	if d.InstanceID != "" {
		t.Fatal("unsafe automatic reattach while old mount live")
	}
}
func TestTopologyInactiveVMAndResize(t *testing.T) {
	e := testkit.New(t)
	v := testkit.Volume(t, e, testkit.ControllerA, "pvc-a")
	i := e.Config.Instances["vm-a2"]
	i.Zone = "ru-1b"
	e.Config.Instances[i.ID] = i
	_, err := e.Call(ctx, testkit.ControllerA, "CreateAttachment", model.Request{VolumeID: v.ID, InstanceID: i.ID})
	denied(t, err, "FailedPrecondition")
	i.Active = false
	e.Config.Instances[i.ID] = i
	_, err = e.Call(ctx, testkit.ControllerA, "CreateAttachment", model.Request{VolumeID: v.ID, InstanceID: i.ID})
	denied(t, err, "PermissionDenied")
	testkit.Call(t, e, testkit.ControllerA, "ResizeVolume", model.Request{ID: v.ID, SizeBytes: 2 << 30})
	testkit.Reconcile(t, e)
	testkit.Call(t, e, testkit.ControllerA, "CreateVolume", testkit.CreateRequest("pvc-a"))
	_, err = e.Call(ctx, testkit.ControllerA, "ResizeVolume", model.Request{ID: v.ID, SizeBytes: 1 << 30})
	denied(t, err, "FailedPrecondition")
}
func TestInitializationCannotAuthorizeReformat(t *testing.T) {
	e := testkit.New(t)
	v, a := testkit.Attached(t, e)
	p := testkit.Proof(v, a)
	testkit.Call(t, e, testkit.NodeA, "AcquireUse", p)
	testkit.Call(t, e, testkit.NodeA, "MarkInitialized", p)
	out := testkit.Call(t, e, testkit.NodeA, "CheckUse", p)
	if out.Volume.AllowFormat {
		t.Fatal("format grant was not revoked")
	}
}
