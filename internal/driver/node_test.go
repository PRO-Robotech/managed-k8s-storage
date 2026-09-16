package driver_test

import (
	"context"
	"github.com/PRO-Robotech/managed-k8s-storage/internal/driver"
	"github.com/PRO-Robotech/managed-k8s-storage/internal/host"
	"github.com/PRO-Robotech/managed-k8s-storage/internal/model"
	"github.com/PRO-Robotech/managed-k8s-storage/internal/store"
	"github.com/PRO-Robotech/managed-k8s-storage/internal/testkit"
	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func capFS(fs string) *csi.VolumeCapability {
	c := &csi.VolumeCapability{AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER}}
	if fs == "block" {
		c.AccessType = &csi.VolumeCapability_Block{Block: &csi.VolumeCapability_BlockVolume{}}
	} else {
		c.AccessType = &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{FsType: fs}}
	}
	return c
}
func code(t *testing.T, e error, w codes.Code) {
	t.Helper()
	if status.Code(e) != w {
		t.Fatalf("want %s got %v", w, e)
	}
}
func TestNodeLifecycleAndRestart(t *testing.T) {
	for _, fs := range []string{"ext4", "xfs", "block"} {
		t.Run(fs, func(t *testing.T) {
			ctx := context.Background()
			e := testkit.New(t)
			r := testkit.CreateRequest("pvc-test")
			if fs == "block" {
				r.Mode = "block"
				r.FSType = ""
			} else {
				r.FSType = fs
			}
			v := testkit.Call(t, e, testkit.ControllerA, "CreateVolume", r).Volume
			testkit.Reconcile(t, e)
			a := testkit.Call(t, e, testkit.ControllerA, "CreateAttachment", model.Request{VolumeID: v.ID, InstanceID: "vm-a"}).Attachment
			testkit.Reconcile(t, e)
			dir := t.TempDir()
			j, err := store.Open(filepath.Join(dir, "journal.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { j.Close() }()
			h, err := store.Open(filepath.Join(dir, "host.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer h.Close()
			api := testkit.Local{E: e, Principal: testkit.NodeA}
			n := &driver.Node{API: api, Host: &host.Simulated{DB: h}, Journal: j, Root: dir, Instance: e.Config.Instances["vm-a"]}
			pc := map[string]string{"attachmentID": a.ID, "attachmentGeneration": strconv.FormatUint(a.Generation, 10)}
			stage := filepath.Join(dir, "stage")
			target := filepath.Join(dir, "pod", "target")
			req := &csi.NodeStageVolumeRequest{VolumeId: v.ID, StagingTargetPath: stage, VolumeCapability: capFS(fs), PublishContext: pc}
			_, err = n.NodeStageVolume(ctx, req)
			code(t, err, codes.OK)
			_, err = n.NodeStageVolume(ctx, req)
			code(t, err, codes.OK)
			pub := &csi.NodePublishVolumeRequest{VolumeId: v.ID, StagingTargetPath: stage, TargetPath: target, VolumeCapability: capFS(fs), PublishContext: pc, VolumeContext: map[string]string{"csi.storage.k8s.io/pod.uid": "pod-123"}}
			_, err = n.NodePublishVolume(ctx, pub)
			code(t, err, codes.OK)
			pub.Readonly = true
			_, err = n.NodePublishVolume(ctx, pub)
			code(t, err, codes.AlreadyExists)
			pub.Readonly = false
			// Restart the agent from disk, while host mounts and attachment use survive.
			file := j.Path()
			j.Close()
			j, err = store.Open(file)
			if err != nil {
				t.Fatal(err)
			}
			n = &driver.Node{API: api, Host: &host.Simulated{DB: h}, Journal: j, Root: dir, Instance: e.Config.Instances["vm-a"]}
			_, err = n.NodePublishVolume(ctx, pub)
			code(t, err, codes.OK)
			_, err = n.NodeUnstageVolume(ctx, &csi.NodeUnstageVolumeRequest{VolumeId: v.ID, StagingTargetPath: stage})
			code(t, err, codes.FailedPrecondition)
			testkit.Call(t, e, testkit.ControllerA, "ResizeVolume", model.Request{ID: v.ID, SizeBytes: 2 << 30})
			testkit.Reconcile(t, e)
			_, err = n.NodeExpandVolume(ctx, &csi.NodeExpandVolumeRequest{VolumeId: v.ID, VolumePath: target, CapacityRange: &csi.CapacityRange{RequiredBytes: 2 << 30}})
			code(t, err, codes.OK)
			stats, err := n.NodeGetVolumeStats(ctx, &csi.NodeGetVolumeStatsRequest{VolumeId: v.ID, VolumePath: stage})
			code(t, err, codes.OK)
			if stats.Usage[0].Total != 2<<30 {
				t.Fatal("resize not visible")
			}
			testkit.Call(t, e, testkit.ControllerA, "DeleteAttachment", model.Request{ID: a.ID})
			testkit.Reconcile(t, e)
			_, err = n.NodePublishVolume(ctx, pub)
			code(t, err, codes.FailedPrecondition)
			_, err = n.NodeUnpublishVolume(ctx, &csi.NodeUnpublishVolumeRequest{VolumeId: v.ID, TargetPath: target})
			code(t, err, codes.OK)
			_, err = n.NodeUnpublishVolume(ctx, &csi.NodeUnpublishVolumeRequest{VolumeId: v.ID, TargetPath: target})
			code(t, err, codes.OK)
			_, err = n.NodeUnstageVolume(ctx, &csi.NodeUnstageVolumeRequest{VolumeId: v.ID, StagingTargetPath: stage})
			code(t, err, codes.OK)
			testkit.Reconcile(t, e)
			disk, _ := e.Provider.GetVolume(ctx, v.ID)
			if disk.InstanceID != "" {
				t.Fatal("disk still attached")
			}
		})
	}
}
func TestNodeRejectsForeignProofBeforeHostEffect(t *testing.T) {
	e := testkit.New(t)
	v, a := testkit.Attached(t, e)
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "node.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	n := &driver.Node{API: testkit.Local{E: e, Principal: testkit.NodeB}, Journal: db, Root: dir} // nil Host panics if authorization is bypassed
	_, err = n.NodeStageVolume(context.Background(), &csi.NodeStageVolumeRequest{VolumeId: v.ID, StagingTargetPath: filepath.Join(dir, "stage"), VolumeCapability: capFS("ext4"), PublishContext: map[string]string{"attachmentID": a.ID, "attachmentGeneration": "1"}})
	code(t, err, codes.PermissionDenied)
}
func TestNodeRejectsPathEscapeAndSymlink(t *testing.T) {
	root := t.TempDir()
	n := &driver.Node{Root: root}
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/etc/target", filepath.Join(root, "escape", "target"), root, root + "/a/../target"} {
		_, err := n.NodeUnpublishVolume(context.Background(), &csi.NodeUnpublishVolumeRequest{VolumeId: "volume-test", TargetPath: path})
		code(t, err, codes.InvalidArgument)
	}
}

type failingUnmount struct{ host.Host }

func (f failingUnmount) Unmount(context.Context, string) error {
	return model.Err("Unavailable", "injected busy mount")
}
func TestUnmountFailureKeepsDetachBarrier(t *testing.T) {
	ctx := context.Background()
	e := testkit.New(t)
	v, a := testkit.Attached(t, e)
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "journal.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	hostDB, err := store.Open(filepath.Join(dir, "host.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer hostDB.Close()
	n := &driver.Node{API: testkit.Local{E: e, Principal: testkit.NodeA}, Journal: db, Root: dir, Host: failingUnmount{Host: &host.Simulated{DB: hostDB}}}
	path := filepath.Join(dir, "stage")
	_, err = n.NodeStageVolume(ctx, &csi.NodeStageVolumeRequest{VolumeId: v.ID, StagingTargetPath: path, VolumeCapability: capFS("ext4"), PublishContext: map[string]string{"attachmentID": a.ID, "attachmentGeneration": "1"}})
	code(t, err, codes.OK)
	testkit.Call(t, e, testkit.ControllerA, "DeleteAttachment", model.Request{ID: a.ID})
	_, err = n.NodeUnstageVolume(ctx, &csi.NodeUnstageVolumeRequest{VolumeId: v.ID, StagingTargetPath: path})
	code(t, err, codes.Unavailable)
	testkit.Reconcile(t, e)
	d, err := e.Provider.GetVolume(ctx, v.ID)
	if err != nil || d.InstanceID != "vm-a" {
		t.Fatal("failed unmount allowed detach", err)
	}
}
