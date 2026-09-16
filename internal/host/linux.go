//go:build linux

package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/PRO-Robotech/managed-k8s-storage/internal/model"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Linux is opt-in. The provider must arrange a stable /dev/disk/by-id entry.
// Prototype operations run in kubelet's mount namespace; no container isolation magic.
type Linux struct {
	DeviceRoot       string
	DiscoveryTimeout time.Duration
}

func run(ctx context.Context, name string, args ...string) (string, error) {
	c := exec.CommandContext(ctx, name, args...)
	b, err := c.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(string(b)))
	}
	return strings.TrimSpace(string(b)), nil
}
func (h *Linux) device(ctx context.Context, v model.Volume) (string, error) {
	if !model.ValidID(v.DeviceKey) {
		return "", fmt.Errorf("invalid trusted device key")
	}
	timeout := h.DiscoveryTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		p, err := filepath.EvalSymlinks(filepath.Join(h.DeviceRoot, v.DeviceKey))
		if err == nil {
			st, e := os.Stat(p)
			if e == nil && st.Mode()&os.ModeDevice != 0 && st.Mode()&os.ModeCharDevice == 0 {
				return p, nil
			}
		}
		select {
		case <-ctx.Done():
			return "", model.Err("Unavailable", "block device discovery pending")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

type mountedFS struct {
	Source  string `json:"source"`
	FSType  string `json:"fstype"`
	Options string `json:"options"`
	Dev     string `json:"maj:min"`
}

func mounted(ctx context.Context, path string) (*mountedFS, error) {
	b, err := exec.CommandContext(ctx, "findmnt", "--json", "--mountpoint", path, "--output", "SOURCE,FSTYPE,OPTIONS,MAJ:MIN").Output()
	if err != nil {
		var x *exec.ExitError
		if errors.As(err, &x) && x.ExitCode() == 1 {
			return nil, nil
		}
		return nil, err
	}
	var out struct {
		FS []mountedFS `json:"filesystems"`
	}
	if err = json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	if len(out.FS) != 1 {
		return nil, fmt.Errorf("ambiguous mountpoint")
	}
	return &out.FS[0], nil
}
func devNumbers(path string) (string, error) {
	var s syscall.Stat_t
	if err := syscall.Stat(path, &s); err != nil {
		return "", err
	}
	r := uint64(s.Rdev)
	major := ((r >> 8) & 0xfff) | ((r >> 32) & 0xfffff000)
	minor := (r & 0xff) | ((r >> 12) & 0xffffff00)
	return fmt.Sprintf("%d:%d", major, minor), nil
}
func readOnly(opts string) bool {
	for _, o := range strings.Split(opts, ",") {
		if o == "ro" {
			return true
		}
	}
	return false
}
func (h *Linux) Stage(ctx context.Context, v model.Volume, path string) error {
	dev, err := h.device(ctx, v)
	if err != nil {
		return err
	}
	if v.Mode == "block" {
		return os.MkdirAll(path, 0750)
	}
	exists, err := mounted(ctx, path)
	if err != nil {
		return err
	}
	if exists != nil {
		numbers, err := devNumbers(dev)
		if err != nil {
			return err
		}
		if exists.Dev != numbers || exists.FSType != v.FSType || readOnly(exists.Options) {
			return fmt.Errorf("existing staging mount differs")
		}
		return nil
	}
	// wipefs detects partition tables as well as filesystems. Never use -f on mkfs.
	b, err := run(ctx, "wipefs", "--no-act", "--json", dev)
	if err != nil {
		return err
	}
	var sig struct {
		Signatures []struct {
			Type string `json:"type"`
		} `json:"signatures"`
	}
	if err = json.Unmarshal([]byte(b), &sig); err != nil {
		return err
	}

	types := make([]string, 0, len(sig.Signatures))
	for _, signature := range sig.Signatures {
		types = append(types, signature.Type)
	}
	format, err := formatRequired(types, v.FSType, v.AllowFormat)
	if err != nil {
		return err
	}
	if format {
		switch v.FSType {
		case "ext4":
			_, err = run(ctx, "mkfs.ext4", dev)
		case "xfs":
			_, err = run(ctx, "mkfs.xfs", dev)
		}
		if err != nil {
			return err
		}
	}

	if err = os.MkdirAll(path, 0750); err != nil {
		return err
	}
	_, err = run(ctx, "mount", "-t", v.FSType, "-o", "nodev,nosuid", dev, path)
	return err
}
func (h *Linux) Publish(ctx context.Context, v model.Volume, stage, target string, ro bool) error {
	source := stage
	var err error
	if v.Mode == "block" {
		source, err = h.device(ctx, v)
		if err != nil {
			return err
		}
	}
	old, err := mounted(ctx, target)
	if err != nil {
		return err
	}
	if old != nil {
		if v.Mode == "block" {
			a, e := devNumbers(source)
			if e != nil {
				return e
			}
			b, e := devNumbers(target)
			if e != nil {
				return e
			}
			if a != b {
				return fmt.Errorf("target block device differs")
			}
		} else {
			a, e := os.Stat(source)
			if e != nil {
				return e
			}
			b, e := os.Stat(target)
			if e != nil {
				return e
			}
			if !os.SameFile(a, b) {
				return fmt.Errorf("target source differs")
			}
		}
		if readOnly(old.Options) != ro {
			return fmt.Errorf("target readonly differs")
		}
		return nil
	}
	if v.Mode == "block" {
		if err = os.MkdirAll(filepath.Dir(target), 0750); err != nil {
			return err
		}
		f, e := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
		if e != nil && !os.IsExist(e) {
			return e
		}
		if e == nil {
			f.Close()
		}
		st, e := os.Lstat(target)
		if e != nil || !st.Mode().IsRegular() {
			return fmt.Errorf("block target must be regular placeholder")
		}
	} else {
		if err = os.MkdirAll(target, 0750); err != nil {
			return err
		}
	}
	if _, err = run(ctx, "mount", "--bind", source, target); err != nil {
		return err
	}
	if ro {
		if _, err = run(ctx, "mount", "-o", "remount,bind,ro", target); err != nil {
			_, _ = run(ctx, "umount", target)
			return err
		}
	}
	return nil
}
func (h *Linux) Unmount(ctx context.Context, path string) error {
	old, err := mounted(ctx, path)
	if err != nil {
		return err
	}
	if old == nil {
		return nil
	}
	_, err = run(ctx, "umount", path)
	return err
}
func (h *Linux) Expand(ctx context.Context, v model.Volume, path string) error {
	dev, err := h.device(ctx, v)
	if err != nil {
		return err
	}
	size, err := run(ctx, "blockdev", "--getsize64", dev)
	if err != nil {
		return err
	}
	n, err := strconv.ParseInt(size, 10, 64)
	if err != nil {
		return err
	}
	if n < v.SizeBytes {
		return model.Err("Unavailable", "device capacity has not grown")
	}
	if v.Mode == "block" {
		return nil
	}
	if err = verifyFilesystemMount(ctx, v, path, dev); err != nil {
		return err
	}
	switch v.FSType {
	case "ext4":
		_, err = run(ctx, "resize2fs", dev)
	case "xfs":
		_, err = run(ctx, "xfs_growfs", path)
	default:
		return fmt.Errorf("unsupported FS")
	}
	return err
}
func (h *Linux) Stats(ctx context.Context, v model.Volume, path string) (Stats, error) {
	if v.Mode == "block" {
		dev, err := h.device(ctx, v)
		if err != nil {
			return Stats{}, err
		}
		s, err := run(ctx, "blockdev", "--getsize64", dev)
		if err != nil {
			return Stats{}, err
		}
		n, err := strconv.ParseInt(s, 10, 64)
		return Stats{Total: n}, err
	}
	dev, err := h.device(ctx, v)
	if err != nil {
		return Stats{}, err
	}
	if err = verifyFilesystemMount(ctx, v, path, dev); err != nil {
		return Stats{}, err
	}
	var s syscall.Statfs_t
	if err := syscall.Statfs(path, &s); err != nil {
		return Stats{}, err
	}
	return Stats{Total: int64(s.Blocks) * s.Bsize, Available: int64(s.Bavail) * s.Bsize, Used: int64(s.Blocks-s.Bfree) * s.Bsize, Inodes: int64(s.Files), InodesFree: int64(s.Ffree)}, nil
}

func formatRequired(signatures []string, fs string, allowed bool) (bool, error) {
	if fs != "ext4" && fs != "xfs" {
		return false, fmt.Errorf("unsupported filesystem")
	}
	if len(signatures) == 0 {
		if !allowed {
			return false, fmt.Errorf("blank device but format grant absent")
		}
		return true, nil
	}
	for _, signature := range signatures {
		if signature != fs {
			return false, fmt.Errorf("existing signature %s differs from %s; refusing format", signature, fs)
		}
	}
	return false, nil
}
func verifyFilesystemMount(ctx context.Context, v model.Volume, path, device string) error {
	m, err := mounted(ctx, path)
	if err != nil {
		return err
	}
	expected, err := devNumbers(device)
	if err != nil {
		return err
	}
	if m == nil || m.Dev != expected || m.FSType != v.FSType {
		return fmt.Errorf("volume filesystem is not mounted at requested path")
	}
	return nil
}
