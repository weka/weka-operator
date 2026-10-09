package blockdev

import (
	"os"
	"path/filepath"
	"testing"
)

// realLsblkJSON is a fixture derived from a live node.
// The node's ~37 type:"loop" entries are trimmed here to 3 representative ones
// (a snap mount, the "/opt/weka/logs" mount, and an unmounted one) since the
// loop-filtering behavior is fully exercised by the dedicated test case below.
// The substance is the 2 type:"disk" nvme devices (/dev/nvme4n1, /dev/nvme6n1):
// each has mountpoint null but a part2 -> md0 (raid1) child that is mounted at
// "/opt/weka/data/agent/sockets/...".
var realLsblkJSON = []byte(`{
   "blockdevices": [
      {"name":"/dev/loop0","type":"loop","mountpoint":"/snap/core20/1611","serial":null,"children":null},
      {"name":"/dev/loop35","type":"loop","mountpoint":"/opt/weka/logs","serial":null,"children":null},
      {"name":"/dev/loop36","type":"loop","mountpoint":null,"serial":null,"children":null},
      {"name":"/dev/nvme4n1","type":"disk","mountpoint":null,"serial":null,"children":[
         {"name":"/dev/nvme4n1p1","type":"part","mountpoint":null,"serial":null,"children":null},
         {"name":"/dev/nvme4n1p2","type":"part","mountpoint":null,"serial":null,"children":[
            {"name":"/dev/md0","type":"raid1","mountpoint":"/opt/weka/data/agent/sockets/000","serial":null,"children":null}
         ]}
      ]},
      {"name":"/dev/nvme6n1","type":"disk","mountpoint":null,"serial":null,"children":[
         {"name":"/dev/nvme6n1p1","type":"part","mountpoint":null,"serial":null,"children":null},
         {"name":"/dev/nvme6n1p2","type":"part","mountpoint":null,"serial":null,"children":[
            {"name":"/dev/md0","type":"raid1","mountpoint":"/opt/weka/data/agent/sockets/001","serial":null,"children":null}
         ]}
      ]}
   ]
}`)

func TestDisksFromLsblk(t *testing.T) {
	tests := []struct {
		name        string
		input       []byte
		wantErr     bool
		wantCount   int
		wantPaths   []string
		wantMounted []bool
	}{
		{
			name:      "real fixture: two nvme disks, both mounted via raid child",
			input:     realLsblkJSON,
			wantCount: 2,
			wantPaths: []string{"/dev/nvme4n1", "/dev/nvme6n1"},
			// IsMounted==true because the recursion bubbles the raid child's mountpoint up
			// two levels through part2 to the disk.
			wantMounted: []bool{true, true},
		},
		{
			name:    "malformed JSON returns error",
			input:   []byte(`{not valid json`),
			wantErr: true,
		},
		{
			name:      "empty blockdevices returns empty slice",
			input:     []byte(`{"blockdevices":[]}`),
			wantCount: 0,
		},
		{
			name: "only loop devices are filtered out",
			input: []byte(`{"blockdevices":[
				{"name":"/dev/loop0","type":"loop","mountpoint":null,"children":null},
				{"name":"/dev/loop1","type":"loop","mountpoint":"/mnt/x","children":null}
			]}`),
			wantCount: 0,
		},
		{
			name: "raid and part top-level types are filtered out",
			input: []byte(`{"blockdevices":[
				{"name":"/dev/md0","type":"raid1","mountpoint":"/data","children":null},
				{"name":"/dev/sda1","type":"part","mountpoint":null,"children":null}
			]}`),
			wantCount: 0,
		},
		{
			name: "unmounted disk",
			input: []byte(`{"blockdevices":[
				{"name":"/dev/sda","type":"disk","mountpoint":null,"children":null}
			]}`),
			wantCount:   1,
			wantPaths:   []string{"/dev/sda"},
			wantMounted: []bool{false},
		},
		{
			name: "disk with direct mountpoint",
			input: []byte(`{"blockdevices":[
				{"name":"/dev/sdb","type":"disk","mountpoint":"/data","children":null}
			]}`),
			wantCount:   1,
			wantPaths:   []string{"/dev/sdb"},
			wantMounted: []bool{true},
		},
		{
			name: "disk with mountpoint only in nested child",
			input: []byte(`{"blockdevices":[
				{"name":"/dev/sdc","type":"disk","mountpoint":null,"children":[
					{"name":"/dev/sdc1","type":"part","mountpoint":null,"children":[
						{"name":"/dev/sdc1a","type":"part","mountpoint":"/boot","children":null}
					]}
				]}
			]}`),
			wantCount:   1,
			wantPaths:   []string{"/dev/sdc"},
			wantMounted: []bool{true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := disksFromLsblk(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("disksFromLsblk(): want error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("disksFromLsblk(): unexpected error: %v", err)
			}
			if len(got) != tt.wantCount {
				t.Fatalf("disksFromLsblk(): got %d disks, want %d; disks=%v", len(got), tt.wantCount, got)
			}
			for i, d := range got {
				if tt.wantPaths != nil && d.Path != tt.wantPaths[i] {
					t.Errorf("disk[%d].Path = %q, want %q", i, d.Path, tt.wantPaths[i])
				}
				if tt.wantMounted != nil && d.IsMounted != tt.wantMounted[i] {
					t.Errorf("disk[%d].IsMounted = %v, want %v", i, d.IsMounted, tt.wantMounted[i])
				}
			}
		})
	}
}

func TestParseUdevSerial(t *testing.T) {
	tests := []struct {
		name  string
		input []byte
		want  string
	}{
		{
			// Primary case: realistic E:-prefixed udev data line.
			// OLD CODE (CutPrefix "ID_SERIAL=") would return "" here — the bug.
			// NEW CODE (substring Contains + index after first "=") returns correct value.
			name:  "E:-prefixed line: Samsung_SSD_970",
			input: []byte("E:ID_SERIAL=Samsung_SSD_970\nE:ID_SERIAL_SHORT=970\n"),
			want:  "Samsung_SSD_970",
		},
		{
			// ID_SERIAL_SHORT= must NOT match: it does not contain "ID_SERIAL=" as a substring
			// because after "ID_SERIAL" comes "_SHORT=", not "=".
			name:  "only ID_SERIAL_SHORT present returns empty",
			input: []byte("E:ID_SERIAL_SHORT=foo\n"),
			want:  "",
		},
		{
			name:  "empty input returns empty",
			input: []byte(""),
			want:  "",
		},
		{
			// First matching line wins when two ID_SERIAL= lines are present.
			name:  "first ID_SERIAL= line wins",
			input: []byte("E:ID_SERIAL=First_Match\nE:ID_SERIAL=Second_Match\n"),
			want:  "First_Match",
		},
		{
			// A bare "ID_SERIAL=bare" (no E: prefix) still works — the match is by substring.
			name:  "bare line without prefix",
			input: []byte("ID_SERIAL=bare\n"),
			want:  "bare",
		},
		{
			// Value is trimmed of surrounding whitespace.
			name:  "value is trimmed",
			input: []byte("E:ID_SERIAL=  spaced  \n"),
			want:  "spaced",
		},
		{
			// A line with ID_SERIAL= but no value returns "".
			name:  "empty value after equals",
			input: []byte("E:ID_SERIAL=\n"),
			want:  "",
		},
		{
			// Mixed: other fields before the serial line.
			name:  "serial line among other udev fields",
			input: []byte("E:DEVTYPE=disk\nE:ID_PATH=pci-0000:00:17.0\nE:ID_SERIAL=WDC_WD40EFRX\nE:ID_MODEL=WDC\n"),
			want:  "WDC_WD40EFRX",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseUdevSerial(tt.input)
			if got != tt.want {
				t.Errorf("parseUdevSerial(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// buildFakeSysfs creates a temp directory laid out like a minimal /sys, with:
//   - an NVMe controller "nvme0" whose model file is at .../nvme0/model, and whose
//     namespace dir .../nvme0/nvme0n1 has a partition dir .../nvme0n1/nvme0n1p1
//     (marked with a "partition" file)
//   - a SCSI/SATA disk "sda" whose model file is at .../sda/device/model, and whose
//     partition dir .../sda/sda1 is marked with a "partition" file
//
// /sys/class/block/<name> entries are symlinks into these device dirs, mirroring
// the real sysfs layout closely enough for ResolveBlockDeviceSysfsPath/GetDeviceModel.
func buildFakeSysfs(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	devices := filepath.Join(root, "devices")
	classBlock := filepath.Join(root, "class", "block")

	dirs := []string{
		filepath.Join(devices, "nvme0", "nvme0n1", "nvme0n1p1"),
		filepath.Join(devices, "sda", "device"),
		filepath.Join(devices, "sda", "sda1"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("MkdirAll(%s): %v", d, err)
		}
	}
	files := map[string]string{
		filepath.Join(devices, "nvme0", "model"):                             "SAMSUNG MZQL2\n",
		filepath.Join(devices, "nvme0", "nvme0n1", "nvme0n1p1", "partition"): "1\n",
		filepath.Join(devices, "sda", "device", "model"):                     "ST1000DM010\n",
		filepath.Join(devices, "sda", "sda1", "partition"):                   "1\n",
	}
	for path, content := range files {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile(%s): %v", path, err)
		}
	}
	links := map[string]string{
		filepath.Join(classBlock, "nvme0n1"):   filepath.Join(devices, "nvme0", "nvme0n1"),
		filepath.Join(classBlock, "nvme0n1p1"): filepath.Join(devices, "nvme0", "nvme0n1", "nvme0n1p1"),
		filepath.Join(classBlock, "sda"):       filepath.Join(devices, "sda"),
		filepath.Join(classBlock, "sda1"):      filepath.Join(devices, "sda", "sda1"),
	}
	if err := os.MkdirAll(classBlock, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", classBlock, err)
	}
	for link, target := range links {
		if err := os.Symlink(target, link); err != nil {
			t.Fatalf("Symlink(%s -> %s): %v", link, target, err)
		}
	}
	return classBlock
}

func TestGetDeviceModel(t *testing.T) {
	classBlock := buildFakeSysfs(t)
	origRoot := sysClassBlockRoot
	sysClassBlockRoot = classBlock
	t.Cleanup(func() { sysClassBlockRoot = origRoot })

	tests := []struct {
		name       string
		devicePath string
		want       string
		wantErr    bool
	}{
		{name: "nvme whole device", devicePath: "/dev/nvme0n1", want: "SAMSUNG MZQL2"},
		{name: "nvme partition falls back to controller model", devicePath: "/dev/nvme0n1p1", want: "SAMSUNG MZQL2"},
		{name: "scsi whole device", devicePath: "/dev/sda", want: "ST1000DM010"},
		{name: "scsi partition falls back to whole-device model", devicePath: "/dev/sda1", want: "ST1000DM010"},
		{name: "unknown device has no model", devicePath: "/dev/nvme9n1", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := GetDeviceModel(tt.devicePath)
			if (err != nil) != tt.wantErr {
				t.Fatalf("GetDeviceModel(%q) error = %v, wantErr %v", tt.devicePath, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("GetDeviceModel(%q) = %q, want %q", tt.devicePath, got, tt.want)
			}
		})
	}
}

func TestResolveDriveModel(t *testing.T) {
	classBlock := buildFakeSysfs(t)
	origRoot := sysClassBlockRoot
	sysClassBlockRoot = classBlock
	t.Cleanup(func() { sysClassBlockRoot = origRoot })

	tests := []struct {
		name                string
		hardwareModel       string
		hardwareModelNumber string
		devicePath          string
		want                string
	}{
		{name: "prefers hardware model", hardwareModel: "Reported Model", devicePath: "/dev/sda", want: "Reported Model"},
		{name: "falls back to model_number", hardwareModelNumber: "Reported Number", devicePath: "/dev/sda", want: "Reported Number"},
		{name: "falls back to sysfs when hardware has neither", devicePath: "/dev/sda", want: "ST1000DM010"},
		{name: "empty when sysfs also has nothing", devicePath: "/dev/nvme9n1", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveDriveModel(tt.hardwareModel, tt.hardwareModelNumber, tt.devicePath)
			if got != tt.want {
				t.Errorf("ResolveDriveModel(%q, %q, %q) = %q, want %q",
					tt.hardwareModel, tt.hardwareModelNumber, tt.devicePath, got, tt.want)
			}
		})
	}
}
