package blockdev

import (
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
