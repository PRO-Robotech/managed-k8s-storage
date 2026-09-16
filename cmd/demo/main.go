// Demo drives the real CSI Unix sockets. It does not pretend to run Kubernetes.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"log"
	"path/filepath"
	"time"
)

func main() {
	controller := flag.String("controller", "", "controller Unix socket")
	node := flag.String("node", "", "node Unix socket")
	root := flag.String("root", "", "simulated kubelet root")
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cc, err := grpc.NewClient("unix://"+*controller, grpc.WithTransportCredentials(insecure.NewCredentials()))
	must(err)
	defer cc.Close()
	nc, err := grpc.NewClient("unix://"+*node, grpc.WithTransportCredentials(insecure.NewCredentials()))
	must(err)
	defer nc.Close()
	c := csi.NewControllerClient(cc)
	n := csi.NewNodeClient(nc)
	info, err := n.NodeGetInfo(ctx, &csi.NodeGetInfoRequest{})
	must(err)
	checks := []string{}
	for _, kind := range []string{"ext4", "xfs", "block"} {
		cap := &csi.VolumeCapability{AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER}}
		if kind == "block" {
			cap.AccessType = &csi.VolumeCapability_Block{Block: &csi.VolumeCapability_BlockVolume{}}
		} else {
			cap.AccessType = &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{FsType: kind}}
		}
		var v *csi.CreateVolumeResponse
		retry(ctx, func() error {
			var e error
			v, e = c.CreateVolume(ctx, &csi.CreateVolumeRequest{Name: "demo-" + kind, VolumeCapabilities: []*csi.VolumeCapability{cap}, CapacityRange: &csi.CapacityRange{RequiredBytes: 1 << 30}})
			return e
		})
		id := v.Volume.VolumeId
		var a *csi.ControllerPublishVolumeResponse
		retry(ctx, func() error {
			var e error
			a, e = c.ControllerPublishVolume(ctx, &csi.ControllerPublishVolumeRequest{VolumeId: id, NodeId: info.NodeId, VolumeCapability: cap})
			return e
		})
		stage := filepath.Join(*root, "stage", kind)
		target := filepath.Join(*root, "pods", "pod-demo", kind)
		stageReq := &csi.NodeStageVolumeRequest{VolumeId: id, StagingTargetPath: stage, VolumeCapability: cap, PublishContext: a.PublishContext}
		_, err = n.NodeStageVolume(ctx, stageReq)
		must(err)
		_, err = n.NodeStageVolume(ctx, stageReq)
		must(err)
		pub := &csi.NodePublishVolumeRequest{VolumeId: id, StagingTargetPath: stage, TargetPath: target, VolumeCapability: cap, PublishContext: a.PublishContext, VolumeContext: map[string]string{"csi.storage.k8s.io/pod.uid": "pod-demo", "csi.storage.k8s.io/pod.name": "demo", "csi.storage.k8s.io/pod.namespace": "default", "csi.storage.k8s.io/serviceAccount.name": "default"}}
		_, err = n.NodePublishVolume(ctx, pub)
		must(err)
		_, err = n.NodePublishVolume(ctx, pub)
		must(err)
		_, err = n.NodeUnstageVolume(ctx, &csi.NodeUnstageVolumeRequest{VolumeId: id, StagingTargetPath: stage})
		expect(err, codes.FailedPrecondition)
		retry(ctx, func() error {
			_, e := c.ControllerExpandVolume(ctx, &csi.ControllerExpandVolumeRequest{VolumeId: id, CapacityRange: &csi.CapacityRange{RequiredBytes: 2 << 30}})
			return e
		})
		_, err = n.NodeExpandVolume(ctx, &csi.NodeExpandVolumeRequest{VolumeId: id, VolumePath: target, CapacityRange: &csi.CapacityRange{RequiredBytes: 2 << 30}})
		must(err)
		stats, err := n.NodeGetVolumeStats(ctx, &csi.NodeGetVolumeStatsRequest{VolumeId: id, VolumePath: stage})
		must(err)
		if stats.Usage[0].Total != 2<<30 {
			log.Fatal("unexpected capacity")
		}
		_, err = c.ControllerUnpublishVolume(ctx, &csi.ControllerUnpublishVolumeRequest{VolumeId: id, NodeId: info.NodeId})
		expect(err, codes.Aborted)
		_, err = n.NodePublishVolume(ctx, pub)
		expect(err, codes.FailedPrecondition)
		_, err = n.NodeUnpublishVolume(ctx, &csi.NodeUnpublishVolumeRequest{VolumeId: id, TargetPath: target})
		must(err)
		_, err = n.NodeUnstageVolume(ctx, &csi.NodeUnstageVolumeRequest{VolumeId: id, StagingTargetPath: stage})
		must(err)
		retry(ctx, func() error {
			_, e := c.ControllerUnpublishVolume(ctx, &csi.ControllerUnpublishVolumeRequest{VolumeId: id, NodeId: info.NodeId})
			return e
		})
		_, err = n.NodeStageVolume(ctx, stageReq)
		expect(err, codes.FailedPrecondition)
		retry(ctx, func() error { _, e := c.DeleteVolume(ctx, &csi.DeleteVolumeRequest{VolumeId: id}); return e })
		checks = append(checks, kind+": create, attach, stage, publish, idempotency, resize, stats, detach barrier, cleanup, stale proof, delete")
	}
	b, _ := json.MarshalIndent(map[string]any{"result": "PASS", "transport": "real CSI gRPC Unix sockets + Storage API mTLS", "provider": "persistent emulator", "host": "simulated (no real mounts)", "checks": checks}, "", "  ")
	fmt.Println(string(b))
}
func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
func expect(err error, c codes.Code) {
	if status.Code(err) != c {
		log.Fatalf("expected %s; got %v", c, err)
	}
}
func retry(ctx context.Context, f func() error) {
	for {
		err := f()
		if err == nil {
			return
		}
		if status.Code(err) != codes.Aborted && status.Code(err) != codes.Unavailable {
			log.Fatal(err)
		}
		select {
		case <-ctx.Done():
			log.Fatal(ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}
