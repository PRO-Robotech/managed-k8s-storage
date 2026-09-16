package driver

import (
	"context"
	"errors"
	"github.com/PRO-Robotech/managed-k8s-storage/internal/model"
	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"net"
	"os"
	"path/filepath"
	"strconv"
)

type Identity struct {
	csi.UnimplementedIdentityServer
}

func (*Identity) GetPluginInfo(context.Context, *csi.GetPluginInfoRequest) (*csi.GetPluginInfoResponse, error) {
	return &csi.GetPluginInfoResponse{Name: model.Driver, VendorVersion: "0.1.0-prototype"}, nil
}
func (*Identity) Probe(context.Context, *csi.ProbeRequest) (*csi.ProbeResponse, error) {
	return &csi.ProbeResponse{}, nil
}
func (*Identity) GetPluginCapabilities(context.Context, *csi.GetPluginCapabilitiesRequest) (*csi.GetPluginCapabilitiesResponse, error) {
	return &csi.GetPluginCapabilitiesResponse{Capabilities: []*csi.PluginCapability{
		{Type: &csi.PluginCapability_Service_{Service: &csi.PluginCapability_Service{Type: csi.PluginCapability_Service_CONTROLLER_SERVICE}}},
		{Type: &csi.PluginCapability_Service_{Service: &csi.PluginCapability_Service{Type: csi.PluginCapability_Service_VOLUME_ACCESSIBILITY_CONSTRAINTS}}},
		{Type: &csi.PluginCapability_VolumeExpansion_{VolumeExpansion: &csi.PluginCapability_VolumeExpansion{Type: csi.PluginCapability_VolumeExpansion_ONLINE}}},
	}}, nil
}
func grpcErr(err error) error {
	if err == nil {
		return nil
	}
	var me *model.Error
	if errors.As(err, &me) {
		m := map[string]codes.Code{"InvalidArgument": codes.InvalidArgument, "PermissionDenied": codes.PermissionDenied, "Unauthenticated": codes.Unauthenticated, "NotFound": codes.NotFound, "AlreadyExists": codes.AlreadyExists, "FailedPrecondition": codes.FailedPrecondition, "Aborted": codes.Aborted, "Unavailable": codes.Unavailable, "Unimplemented": codes.Unimplemented}
		if c, ok := m[me.Code]; ok {
			return status.Error(c, me.Message)
		}
	}
	if errors.Is(err, context.Canceled) {
		return status.Error(codes.Canceled, "request canceled")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return status.Error(codes.DeadlineExceeded, "request deadline")
	}
	return status.Error(codes.Internal, err.Error())
}
func capability(c *csi.VolumeCapability) (mode, fs string, err error) {
	if c == nil || c.AccessMode == nil || c.AccessMode.Mode != csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER {
		return "", "", status.Error(codes.InvalidArgument, "SINGLE_NODE_WRITER capability required")
	}
	if c.GetBlock() != nil {
		return "block", "", nil
	}
	m := c.GetMount()
	if m == nil {
		return "", "", status.Error(codes.InvalidArgument, "mount or block required")
	}
	fs = m.FsType
	if fs == "" {
		fs = "ext4"
	}
	if fs != "ext4" && fs != "xfs" {
		return "", "", status.Error(codes.InvalidArgument, "only ext4 and xfs supported")
	}
	if len(m.MountFlags) > 0 || m.VolumeMountGroup != "" {
		return "", "", status.Error(codes.InvalidArgument, "custom mount flags and mount groups not supported")
	}
	return "mount", fs, nil
}
func proof(volume string, pc map[string]string) (model.Request, error) {
	g, err := strconv.ParseUint(pc["attachmentGeneration"], 10, 64)
	if err != nil || g == 0 || !model.ValidID(pc["attachmentID"]) {
		return model.Request{}, status.Error(codes.PermissionDenied, "valid attachment proof required")
	}
	return model.Request{VolumeID: volume, AttachmentID: pc["attachmentID"], Generation: g}, nil
}
func Serve(ctx context.Context, path string, controller *Controller, node *Node) error {
	if !filepath.IsAbs(path) {
		return errors.New("absolute CSI socket path required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	// Never unlink an existing live socket or non-socket automatically.
	l, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	defer l.Close()
	if err = os.Chmod(path, 0600); err != nil {
		return err
	}
	s := grpc.NewServer()
	csi.RegisterIdentityServer(s, &Identity{})
	if controller != nil {
		csi.RegisterControllerServer(s, controller)
	}
	if node != nil {
		csi.RegisterNodeServer(s, node)
	}
	go func() { <-ctx.Done(); s.Stop() }()
	return s.Serve(l)
}
