package model

import (
	"fmt"
	"regexp"
)

const Driver = "storage.example.cloud"
const RegionKey = "topology.storage.example.cloud/region"
const ZoneKey = "topology.storage.example.cloud/zone"

var identifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)

func ValidID(s string) bool { return identifier.MatchString(s) }

type Principal struct{ Role, TenantID, ClusterID, InstanceID string }
type Instance struct {
	ID, TenantID, ClusterID, Region, Zone string
	Active                                bool
}
type Config struct {
	Principals map[string]Principal `json:"principals"`
	Instances  map[string]Instance  `json:"instances"`
	Zones      map[string][]string  `json:"zones"`
}
type Volume struct {
	ID, TenantID, ClusterID, Name, Type, Region, Zone, State string
	Mode, FSType, DeviceKey                                  string
	InitialSizeBytes, SizeBytes, DesiredSizeBytes            int64
	Generation                                               uint64
	AllowFormat, DeleteRequested                             bool
	LastError                                                string
}
type Attachment struct {
	ID, TenantID, ClusterID, VolumeID, InstanceID, State string
	Generation                                           uint64
	DeleteRequested                                      bool
	Uses                                                 map[string]bool
	LastError                                            string
}
type Request struct {
	ID, Name, Type, Region, Zone, Mode, FSType                                            string
	VolumeID, InstanceID, AttachmentID, UseID, PodUID, PodName, Namespace, ServiceAccount string
	SizeBytes                                                                             int64
	Generation                                                                            uint64
}
type Response struct {
	Volume     *Volume     `json:",omitempty"`
	Attachment *Attachment `json:",omitempty"`
	Instance   *Instance   `json:",omitempty"`
}
type Error struct{ Code, Message string }

func (e *Error) Error() string       { return fmt.Sprintf("%s: %s", e.Code, e.Message) }
func Err(code, message string) error { return &Error{Code: code, Message: message} }
