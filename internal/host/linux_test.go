//go:build linux

package host

import "testing"

func TestNonDestructiveFormatPolicy(t *testing.T) {
	for _, c := range []struct {
		name, fs              string
		signatures            []string
		allowed, format, fail bool
	}{
		{name: "new ext4", fs: "ext4", allowed: true, format: true},
		{name: "new xfs", fs: "xfs", allowed: true, format: true},
		{name: "previously initialized blank", fs: "ext4", fail: true},
		{name: "existing ext4", fs: "ext4", signatures: []string{"ext4"}},
		{name: "existing xfs", fs: "xfs", signatures: []string{"xfs"}, allowed: true},
		{name: "filesystem mismatch", fs: "ext4", signatures: []string{"xfs"}, allowed: true, fail: true},
		{name: "partition table", fs: "ext4", signatures: []string{"gpt"}, allowed: true, fail: true},
		{name: "mixed signatures", fs: "xfs", signatures: []string{"xfs", "gpt"}, allowed: true, fail: true},
		{name: "unsupported fs", fs: "btrfs", allowed: true, fail: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			format, err := formatRequired(c.signatures, c.fs, c.allowed)
			if (err != nil) != c.fail || format != c.format {
				t.Fatalf("format=%v err=%v", format, err)
			}
		})
	}
}
