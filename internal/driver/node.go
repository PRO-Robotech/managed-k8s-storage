package driver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/PRO-Robotech/managed-k8s-storage/internal/host"
	"github.com/PRO-Robotech/managed-k8s-storage/internal/model"
	"github.com/PRO-Robotech/managed-k8s-storage/internal/store"
	"github.com/PRO-Robotech/managed-k8s-storage/internal/transport"
	"github.com/container-storage-interface/spec/lib/go/csi"
	bolt "go.etcd.io/bbolt"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type Target struct {
	ReadOnly bool
	PodUID   string
}
type Stage struct {
	Volume  model.Volume
	Proof   model.Request
	Path    string
	Ready   bool
	Targets map[string]Target
}
type Node struct {
	csi.UnimplementedNodeServer
	API      transport.Caller
	Host     host.Host
	Journal  *store.DB
	Root     string
	Instance model.Instance
	mu       sync.Mutex
}

func (n *Node) read() (map[string]*Stage, error) {
	m := map[string]*Stage{}
	err := n.Journal.View(func(tx *bolt.Tx) error { return store.Read(tx, "stages", &m) })
	return m, err
}
func (n *Node) save(m map[string]*Stage) error {
	return n.Journal.Update(func(tx *bolt.Tx) error { return store.Write(tx, "stages", m) })
}
func (n *Node) validPath(path string) error {
	root := filepath.Clean(n.Root)
	if !filepath.IsAbs(path) || path != filepath.Clean(path) || !strings.HasPrefix(path, root+string(os.PathSeparator)) {
		return status.Error(codes.InvalidArgument, "path must be a canonical descendant of kubelet root")
	}
	// Reject symlink components even when final target does not exist yet.
	for p := path; ; p = filepath.Dir(p) {
		st, err := os.Lstat(p)
		if err != nil && !os.IsNotExist(err) {
			return grpcErr(err)
		}
		if err == nil && st.Mode()&os.ModeSymlink != 0 {
			return status.Error(codes.InvalidArgument, "symlink path component rejected")
		}
		if p == "/" {
			break
		}
	}
	return nil
}
func match(s *Stage, mode, fs, path string, p model.Request) bool {
	return s.Path == path && s.Volume.Mode == mode && s.Volume.FSType == fs && s.Proof.AttachmentID == p.AttachmentID && s.Proof.Generation == p.Generation
}
func useID(volume, path string, g uint64) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d", volume, path, g)))
	return hex.EncodeToString(h[:])
}
func (n *Node) NodeStageVolume(ctx context.Context, r *csi.NodeStageVolumeRequest) (*csi.NodeStageVolumeResponse, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if r.VolumeId == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID required")
	}
	if err := n.validPath(r.StagingTargetPath); err != nil {
		return nil, err
	}
	mode, fs, err := capability(r.VolumeCapability)
	if err != nil {
		return nil, err
	}
	p, err := proof(r.VolumeId, r.PublishContext)
	if err != nil {
		return nil, err
	}
	p.UseID = useID(r.VolumeId, r.StagingTargetPath, p.Generation)
	p.Mode, p.FSType = mode, fs
	stages, err := n.read()
	if err != nil {
		return nil, grpcErr(err)
	}
	s := stages[r.VolumeId]
	if s != nil && !match(s, mode, fs, r.StagingTargetPath, p) {
		return nil, status.Error(codes.AlreadyExists, "volume staged with different configuration")
	}
	for id, other := range stages {
		if id != r.VolumeId && (other.Path == r.StagingTargetPath || other.Targets[r.StagingTargetPath].PodUID != "") {
			return nil, status.Error(codes.AlreadyExists, "path already owned by another volume")
		}
	}
	out, err := n.API.Call(ctx, "AcquireUse", p)
	if err != nil {
		return nil, grpcErr(err)
	}
	v := out.Volume
	if v.Mode != mode || v.FSType != fs {
		return nil, status.Error(codes.InvalidArgument, "requested capability differs from authorized volume")
	}
	if s == nil {
		s = &Stage{Volume: *v, Proof: p, Path: r.StagingTargetPath, Targets: map[string]Target{}}
		stages[r.VolumeId] = s
	}
	s.Volume = *v
	if err = n.save(stages); err != nil {
		return nil, grpcErr(err)
	} // persist intent before mount
	if err = n.Host.Stage(ctx, *v, s.Path); err != nil {
		return nil, grpcErr(err)
	}
	if _, err = n.API.Call(ctx, "MarkInitialized", s.Proof); err != nil {
		return nil, grpcErr(err)
	}
	s.Volume.AllowFormat = false
	s.Ready = true
	if err = n.save(stages); err != nil {
		return nil, grpcErr(err)
	}
	return &csi.NodeStageVolumeResponse{}, nil
}
func (n *Node) NodePublishVolume(ctx context.Context, r *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if r.VolumeId == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID required")
	}
	if err := n.validPath(r.TargetPath); err != nil {
		return nil, err
	}
	if err := n.validPath(r.StagingTargetPath); err != nil {
		return nil, err
	}
	if r.TargetPath == r.StagingTargetPath {
		return nil, status.Error(codes.InvalidArgument, "stage and target must differ")
	}
	mode, fs, err := capability(r.VolumeCapability)
	if err != nil {
		return nil, err
	}
	p, err := proof(r.VolumeId, r.PublishContext)
	if err != nil {
		return nil, err
	}
	stages, err := n.read()
	if err != nil {
		return nil, grpcErr(err)
	}
	s := stages[r.VolumeId]
	if s == nil || !s.Ready || !match(s, mode, fs, r.StagingTargetPath, p) {
		return nil, status.Error(codes.FailedPrecondition, "matching stage required")
	}
	pod := r.VolumeContext["csi.storage.k8s.io/pod.uid"]
	if !model.ValidID(pod) {
		return nil, status.Error(codes.InvalidArgument, "Pod UID required via podInfoOnMount")
	}
	for id, other := range stages {
		_, used := other.Targets[r.TargetPath]
		if other.Path == r.TargetPath || (id != r.VolumeId && used) {
			return nil, status.Error(codes.AlreadyExists, "target path in use")
		}
	}
	wanted := Target{ReadOnly: r.Readonly, PodUID: pod}
	if old, ok := s.Targets[r.TargetPath]; ok && old != wanted {
		return nil, status.Error(codes.AlreadyExists, "target configuration differs")
	}
	req := s.Proof
	req.PodUID = pod
	req.PodName = r.VolumeContext["csi.storage.k8s.io/pod.name"]
	req.Namespace = r.VolumeContext["csi.storage.k8s.io/pod.namespace"]
	req.ServiceAccount = r.VolumeContext["csi.storage.k8s.io/serviceAccount.name"]
	if _, err = n.API.Call(ctx, "RecordPublish", req); err != nil {
		return nil, grpcErr(err)
	}
	s.Targets[r.TargetPath] = wanted
	if err = n.save(stages); err != nil {
		return nil, grpcErr(err)
	}
	if err = n.Host.Publish(ctx, s.Volume, s.Path, r.TargetPath, r.Readonly); err != nil {
		return nil, grpcErr(err)
	}
	return &csi.NodePublishVolumeResponse{}, nil
}
func (n *Node) NodeUnpublishVolume(ctx context.Context, r *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if r.VolumeId == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID required")
	}
	if err := n.validPath(r.TargetPath); err != nil {
		return nil, err
	}
	stages, err := n.read()
	if err != nil {
		return nil, grpcErr(err)
	}
	s := stages[r.VolumeId]
	if s == nil {
		return &csi.NodeUnpublishVolumeResponse{}, nil
	}
	if _, ok := s.Targets[r.TargetPath]; !ok {
		return &csi.NodeUnpublishVolumeResponse{}, nil
	}
	if err = n.Host.Unmount(ctx, r.TargetPath); err != nil {
		return nil, grpcErr(err)
	}
	delete(s.Targets, r.TargetPath)
	if err = n.save(stages); err != nil {
		return nil, grpcErr(err)
	}
	return &csi.NodeUnpublishVolumeResponse{}, nil
}
func (n *Node) NodeUnstageVolume(ctx context.Context, r *csi.NodeUnstageVolumeRequest) (*csi.NodeUnstageVolumeResponse, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if r.VolumeId == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID required")
	}
	if err := n.validPath(r.StagingTargetPath); err != nil {
		return nil, err
	}
	stages, err := n.read()
	if err != nil {
		return nil, grpcErr(err)
	}
	s := stages[r.VolumeId]
	if s == nil {
		return &csi.NodeUnstageVolumeResponse{}, nil
	}
	if s.Path != r.StagingTargetPath {
		return nil, status.Error(codes.InvalidArgument, "staging path mismatch")
	}
	if len(s.Targets) > 0 {
		return nil, status.Error(codes.FailedPrecondition, "published targets remain")
	}
	if err = n.Host.Unmount(ctx, s.Path); err != nil {
		return nil, grpcErr(err)
	}
	if _, err = n.API.Call(ctx, "ReleaseUse", s.Proof); err != nil {
		return nil, grpcErr(err)
	}
	delete(stages, r.VolumeId)
	if err = n.save(stages); err != nil {
		return nil, grpcErr(err)
	}
	return &csi.NodeUnstageVolumeResponse{}, nil
}
func (n *Node) lookup(volume, path string) (*Stage, error) {
	if volume == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID required")
	}
	if err := n.validPath(path); err != nil {
		return nil, err
	}
	m, err := n.read()
	if err != nil {
		return nil, grpcErr(err)
	}
	s := m[volume]
	if s == nil || !s.Ready {
		return nil, status.Error(codes.NotFound, "volume not staged")
	}
	_, target := s.Targets[path]
	if s.Path != path && !target {
		return nil, status.Error(codes.NotFound, "untracked volume path")
	}
	return s, nil
}
func (n *Node) NodeExpandVolume(ctx context.Context, r *csi.NodeExpandVolumeRequest) (*csi.NodeExpandVolumeResponse, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if r.CapacityRange == nil || r.CapacityRange.RequiredBytes <= 0 {
		return nil, status.Error(codes.InvalidArgument, "positive capacity required")
	}
	s, err := n.lookup(r.VolumeId, r.VolumePath)
	if err != nil {
		return nil, err
	}
	o, err := n.API.Call(ctx, "CheckUse", s.Proof)
	if err != nil {
		return nil, grpcErr(err)
	}
	v := o.Volume
	if v.SizeBytes < r.CapacityRange.RequiredBytes {
		return nil, status.Error(codes.FailedPrecondition, "controller resize incomplete")
	}
	if r.CapacityRange.LimitBytes > 0 && v.SizeBytes > r.CapacityRange.LimitBytes {
		return nil, status.Error(codes.OutOfRange, "capacity exceeds limit")
	}
	if err = n.Host.Expand(ctx, *v, s.Path); err != nil {
		return nil, grpcErr(err)
	}
	m, err := n.read()
	if err != nil {
		return nil, grpcErr(err)
	}
	m[r.VolumeId].Volume = *v
	if err = n.save(m); err != nil {
		return nil, grpcErr(err)
	}
	return &csi.NodeExpandVolumeResponse{CapacityBytes: v.SizeBytes}, nil
}
func (n *Node) NodeGetVolumeStats(ctx context.Context, r *csi.NodeGetVolumeStatsRequest) (*csi.NodeGetVolumeStatsResponse, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	s, err := n.lookup(r.VolumeId, r.VolumePath)
	if err != nil {
		return nil, err
	}
	v, err := n.Host.Stats(ctx, s.Volume, r.VolumePath)
	if err != nil {
		return nil, grpcErr(err)
	}
	out := &csi.NodeGetVolumeStatsResponse{Usage: []*csi.VolumeUsage{{Unit: csi.VolumeUsage_BYTES, Total: v.Total, Available: v.Available, Used: v.Used}}}
	if s.Volume.Mode == "mount" {
		out.Usage = append(out.Usage, &csi.VolumeUsage{Unit: csi.VolumeUsage_INODES, Total: v.Inodes, Available: v.InodesFree, Used: v.Inodes - v.InodesFree})
	}
	return out, nil
}
func (n *Node) NodeGetInfo(context.Context, *csi.NodeGetInfoRequest) (*csi.NodeGetInfoResponse, error) {
	return &csi.NodeGetInfoResponse{NodeId: n.Instance.ID, AccessibleTopology: &csi.Topology{Segments: map[string]string{model.RegionKey: n.Instance.Region, model.ZoneKey: n.Instance.Zone}}}, nil
}
func (*Node) NodeGetCapabilities(context.Context, *csi.NodeGetCapabilitiesRequest) (*csi.NodeGetCapabilitiesResponse, error) {
	out := &csi.NodeGetCapabilitiesResponse{}
	for _, t := range []csi.NodeServiceCapability_RPC_Type{csi.NodeServiceCapability_RPC_STAGE_UNSTAGE_VOLUME, csi.NodeServiceCapability_RPC_EXPAND_VOLUME, csi.NodeServiceCapability_RPC_GET_VOLUME_STATS} {
		out.Capabilities = append(out.Capabilities, &csi.NodeServiceCapability{Type: &csi.NodeServiceCapability_Rpc{Rpc: &csi.NodeServiceCapability_RPC{Type: t}}})
	}
	return out, nil
}
