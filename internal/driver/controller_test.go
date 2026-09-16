package driver_test

import (
	"context"
	"github.com/PRO-Robotech/managed-k8s-storage/internal/driver"
	"github.com/PRO-Robotech/managed-k8s-storage/internal/model"
	"github.com/PRO-Robotech/managed-k8s-storage/internal/testkit"
	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"testing"
)

func TestControllerCSIContract(t *testing.T) {
	ctx := context.Background()
	e := testkit.New(t)
	c := &driver.Controller{API: testkit.Local{E: e, Principal: testkit.ControllerA}, Region: "ru-1", Zone: "ru-1a"}
	top := &csi.Topology{Segments: map[string]string{model.RegionKey: "ru-1", model.ZoneKey: "ru-1a"}}
	bad := &csi.Topology{Segments: map[string]string{model.RegionKey: "ru-1", model.ZoneKey: "ru-1b"}}
	r := &csi.CreateVolumeRequest{Name: "pvc-csi", VolumeCapabilities: []*csi.VolumeCapability{capFS("ext4")}, AccessibilityRequirements: &csi.TopologyRequirement{Preferred: []*csi.Topology{bad, top}, Requisite: []*csi.Topology{top}}}
	_, err := c.CreateVolume(ctx, r)
	code(t, err, codes.Aborted)
	testkit.Reconcile(t, e)
	out, err := c.CreateVolume(ctx, r)
	code(t, err, codes.OK)
	v := out.Volume
	if v.AccessibleTopology[0].Segments[model.ZoneKey] != "ru-1a" {
		t.Fatal("preferred escaped requisite topology")
	}
	pub := &csi.ControllerPublishVolumeRequest{VolumeId: v.VolumeId, NodeId: "vm-a", VolumeCapability: capFS("ext4")}
	_, err = c.ControllerPublishVolume(ctx, pub)
	code(t, err, codes.Aborted)
	testkit.Reconcile(t, e)
	a, err := c.ControllerPublishVolume(ctx, pub)
	code(t, err, codes.OK)
	if a.PublishContext["attachmentID"] == "" {
		t.Fatal("attachment proof absent")
	}
	// CSI allows omitted node ID: single-writer implementation detaches its only attachment.
	_, err = c.ControllerUnpublishVolume(ctx, &csi.ControllerUnpublishVolumeRequest{VolumeId: v.VolumeId})
	code(t, err, codes.Aborted)
	testkit.Reconcile(t, e)
	_, err = c.ControllerUnpublishVolume(ctx, &csi.ControllerUnpublishVolumeRequest{VolumeId: v.VolumeId})
	code(t, err, codes.OK)
	_, err = c.DeleteVolume(ctx, &csi.DeleteVolumeRequest{VolumeId: v.VolumeId})
	code(t, err, codes.Aborted)
	testkit.Reconcile(t, e)
	_, err = c.DeleteVolume(ctx, &csi.DeleteVolumeRequest{VolumeId: v.VolumeId})
	code(t, err, codes.OK)
}
