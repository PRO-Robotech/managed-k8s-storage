package driver

import (
	"context"
	"github.com/PRO-Robotech/managed-k8s-storage/internal/model"
	"github.com/PRO-Robotech/managed-k8s-storage/internal/transport"
	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"strconv"
)

type Controller struct {
	csi.UnimplementedControllerServer
	API          transport.Caller
	Region, Zone string
}

func (c *Controller) CreateVolume(ctx context.Context, r *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	if r.Name == "" || len(r.VolumeCapabilities) == 0 {
		return nil, status.Error(codes.InvalidArgument, "name and capabilities required")
	}
	if r.VolumeContentSource != nil {
		return nil, status.Error(codes.InvalidArgument, "snapshots and clones unsupported")
	}
	mode, fs, err := capability(r.VolumeCapabilities[0])
	if err != nil {
		return nil, err
	}
	for _, cap := range r.VolumeCapabilities {
		m, f, e := capability(cap)
		if e != nil {
			return nil, e
		}
		if m != mode || f != fs {
			return nil, status.Error(codes.InvalidArgument, "inconsistent capabilities")
		}
	}
	region, zone := c.Region, c.Zone
	if ar := r.AccessibilityRequirements; ar != nil {
		chosen := false
		for _, set := range [][]*csi.Topology{ar.Preferred, ar.Requisite} {
			for _, t := range set {
				rr, zz := t.Segments[model.RegionKey], t.Segments[model.ZoneKey]
				if rr == "" || zz == "" {
					continue
				}
				allowed := len(ar.Requisite) == 0
				for _, req := range ar.Requisite {
					match := true
					for k, v := range req.Segments {
						if t.Segments[k] != v {
							match = false
						}
					}
					allowed = allowed || match
				}
				if allowed {
					region, zone, chosen = rr, zz, true
					break
				}
			}
			if chosen {
				break
			}
		}
		if !chosen && (len(ar.Preferred) > 0 || len(ar.Requisite) > 0) {
			return nil, status.Error(codes.InvalidArgument, "no supported topology")
		}
	}
	size := int64(1 << 30)
	if r.CapacityRange != nil {
		if r.CapacityRange.RequiredBytes < 0 || r.CapacityRange.LimitBytes < 0 {
			return nil, status.Error(codes.InvalidArgument, "negative capacity")
		}
		if r.CapacityRange.RequiredBytes > 0 {
			size = r.CapacityRange.RequiredBytes
		}
		if r.CapacityRange.LimitBytes > 0 && size > r.CapacityRange.LimitBytes {
			return nil, status.Error(codes.OutOfRange, "capacity exceeds limit")
		}
	}
	typ := "standard"
	for k, v := range r.Parameters {
		switch k {
		case "type":
			typ = v
		case "csi.storage.k8s.io/pvc/name", "csi.storage.k8s.io/pvc/namespace", "csi.storage.k8s.io/pv/name":
		default:
			return nil, status.Error(codes.InvalidArgument, "unsupported StorageClass parameter: "+k)
		}
	}
	out, err := c.API.Call(ctx, "CreateVolume", model.Request{Name: r.Name, SizeBytes: size, Type: typ, Region: region, Zone: zone, Mode: mode, FSType: fs})
	if err != nil {
		return nil, grpcErr(err)
	}
	v := out.Volume
	if v.State != "AVAILABLE" || v.LastError != "" {
		return nil, status.Error(codes.Aborted, "volume provisioning pending")
	}
	return &csi.CreateVolumeResponse{Volume: &csi.Volume{VolumeId: v.ID, CapacityBytes: v.SizeBytes, AccessibleTopology: []*csi.Topology{{Segments: map[string]string{model.RegionKey: v.Region, model.ZoneKey: v.Zone}}}}}, nil
}
func (c *Controller) DeleteVolume(ctx context.Context, r *csi.DeleteVolumeRequest) (*csi.DeleteVolumeResponse, error) {
	if r.VolumeId == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID required")
	}
	out, err := c.API.Call(ctx, "DeleteVolume", model.Request{ID: r.VolumeId})
	if err != nil {
		return nil, grpcErr(err)
	}
	if out.Volume != nil && out.Volume.State != "DELETED" {
		return nil, status.Error(codes.Aborted, "deletion pending")
	}
	return &csi.DeleteVolumeResponse{}, nil
}
func (c *Controller) ControllerPublishVolume(ctx context.Context, r *csi.ControllerPublishVolumeRequest) (*csi.ControllerPublishVolumeResponse, error) {
	if r.VolumeId == "" || r.NodeId == "" {
		return nil, status.Error(codes.InvalidArgument, "volume and node IDs required")
	}
	mode, fs, err := capability(r.VolumeCapability)
	if err != nil {
		return nil, err
	}
	if r.Readonly {
		return nil, status.Error(codes.InvalidArgument, "controller readonly unsupported for SINGLE_NODE_WRITER")
	}
	v, err := c.API.Call(ctx, "GetVolume", model.Request{ID: r.VolumeId})
	if err != nil {
		return nil, grpcErr(err)
	}
	if v.Volume.Mode != mode || v.Volume.FSType != fs {
		return nil, status.Error(codes.InvalidArgument, "capability differs from volume")
	}
	out, err := c.API.Call(ctx, "CreateAttachment", model.Request{VolumeID: r.VolumeId, InstanceID: r.NodeId})
	if err != nil {
		return nil, grpcErr(err)
	}
	a := out.Attachment
	if a.State != "ATTACHED" || a.LastError != "" {
		return nil, status.Error(codes.Aborted, "attachment pending")
	}
	return &csi.ControllerPublishVolumeResponse{PublishContext: map[string]string{"attachmentID": a.ID, "attachmentGeneration": strconv.FormatUint(a.Generation, 10)}}, nil
}
func (c *Controller) ControllerUnpublishVolume(ctx context.Context, r *csi.ControllerUnpublishVolumeRequest) (*csi.ControllerUnpublishVolumeResponse, error) {
	if r.VolumeId == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID required")
	}
	out, err := c.API.Call(ctx, "DeleteAttachment", model.Request{VolumeID: r.VolumeId, InstanceID: r.NodeId})
	if err != nil {
		return nil, grpcErr(err)
	}
	if out.Attachment != nil && out.Attachment.State != "DETACHED" {
		return nil, status.Error(codes.Aborted, "detach pending: waiting for node cleanup or provider")
	}
	return &csi.ControllerUnpublishVolumeResponse{}, nil
}
func (c *Controller) ValidateVolumeCapabilities(ctx context.Context, r *csi.ValidateVolumeCapabilitiesRequest) (*csi.ValidateVolumeCapabilitiesResponse, error) {
	if r.VolumeId == "" || len(r.VolumeCapabilities) == 0 {
		return nil, status.Error(codes.InvalidArgument, "volume and capabilities required")
	}
	o, err := c.API.Call(ctx, "GetVolume", model.Request{ID: r.VolumeId})
	if err != nil {
		return nil, grpcErr(err)
	}
	for _, cap := range r.VolumeCapabilities {
		m, f, err := capability(cap)
		if err != nil || m != o.Volume.Mode || f != o.Volume.FSType {
			return &csi.ValidateVolumeCapabilitiesResponse{Message: "capability unsupported"}, nil
		}
	}
	return &csi.ValidateVolumeCapabilitiesResponse{Confirmed: &csi.ValidateVolumeCapabilitiesResponse_Confirmed{VolumeCapabilities: r.VolumeCapabilities, VolumeContext: r.VolumeContext, Parameters: r.Parameters}}, nil
}
func (c *Controller) ControllerExpandVolume(ctx context.Context, r *csi.ControllerExpandVolumeRequest) (*csi.ControllerExpandVolumeResponse, error) {
	if r.VolumeId == "" || r.CapacityRange == nil || r.CapacityRange.RequiredBytes <= 0 {
		return nil, status.Error(codes.InvalidArgument, "volume ID and positive size required")
	}
	if r.CapacityRange.LimitBytes > 0 && r.CapacityRange.RequiredBytes > r.CapacityRange.LimitBytes {
		return nil, status.Error(codes.OutOfRange, "capacity exceeds limit")
	}
	o, err := c.API.Call(ctx, "ResizeVolume", model.Request{ID: r.VolumeId, SizeBytes: r.CapacityRange.RequiredBytes})
	if err != nil {
		return nil, grpcErr(err)
	}
	if o.Volume.SizeBytes < r.CapacityRange.RequiredBytes || o.Volume.LastError != "" {
		return nil, status.Error(codes.Aborted, "resize pending")
	}
	return &csi.ControllerExpandVolumeResponse{CapacityBytes: o.Volume.SizeBytes, NodeExpansionRequired: o.Volume.Mode == "mount"}, nil
}
func (*Controller) ControllerGetCapabilities(context.Context, *csi.ControllerGetCapabilitiesRequest) (*csi.ControllerGetCapabilitiesResponse, error) {
	out := &csi.ControllerGetCapabilitiesResponse{}
	for _, t := range []csi.ControllerServiceCapability_RPC_Type{csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME, csi.ControllerServiceCapability_RPC_PUBLISH_UNPUBLISH_VOLUME, csi.ControllerServiceCapability_RPC_EXPAND_VOLUME} {
		out.Capabilities = append(out.Capabilities, &csi.ControllerServiceCapability{Type: &csi.ControllerServiceCapability_Rpc{Rpc: &csi.ControllerServiceCapability_RPC{Type: t}}})
	}
	return out, nil
}
