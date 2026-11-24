package scanner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/types/descriptorpb"
)

func TestSetFileOption(t *testing.T) {
	files := []*descriptorpb.FileDescriptorProto{
		{Name: new("a.proto"), Syntax: new("proto3"), Package: new("a")},
		{
			Name: new("b.proto"), Syntax: new("proto3"), Package: new("b"),
			Options: &descriptorpb.FileOptions{GoPackage: new("example.com/old"), JavaPackage: new("com.example.b")},
		},
	}
	s := New(nil)
	if err := s.ScanFile(writeDescriptors(t, files...)); err != nil {
		t.Fatalf("ScanFile: %v", err)
	}
	for _, opt := range [][2]string{
		{"go_package", "gbarr.dev/old"},
		{"go_package", "gbarr.dev/iwork/proto"}, // the last value set wins
		{"cc_enable_arenas", "false"},
		{"optimize_for", "SPEED"}, //nolint:misspell // the option's name in descriptor.proto
	} {
		if err := s.SetFileOption(opt[0], opt[1]); err != nil {
			t.Fatalf("SetFileOption(%q, %q): %v", opt[0], opt[1], err)
		}
	}

	var buf strings.Builder
	if err := s.Dump(&buf); err != nil {
		t.Fatalf("Dump: %v", err)
	}
	//nolint:misspell // optimize_for is the option's name in descriptor.proto
	want := `// a.proto
syntax = "proto3";

package a;

option optimize_for = SPEED;
option go_package = "gbarr.dev/iwork/proto";
option cc_enable_arenas = false;

// b.proto
syntax = "proto3";

package b;

option java_package = "com.example.b";
option optimize_for = SPEED;
option go_package = "gbarr.dev/iwork/proto";
option cc_enable_arenas = false;
`
	if got := buf.String(); got != want {
		t.Errorf("Dump output:\n%s\nwant:\n%s", got, want)
	}

	out := t.TempDir()
	if n, err := s.WriteFiles(out); err != nil || n != len(files) {
		t.Fatalf("WriteFiles = %d, %v, want %d, nil", n, err, len(files))
	}
	got, err := os.ReadFile(filepath.Join(out, "b.proto"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `option go_package = "gbarr.dev/iwork/proto";`) {
		t.Errorf("b.proto does not have the go_package set:\n%s", got)
	}
}

func TestSetFileOptionErrors(t *testing.T) {
	tests := []struct {
		name, value, want string
	}{
		{"no_such_option", "x", `unknown file option "no_such_option"`},
		{"cc_enable_arenas", "maybe", `file option cc_enable_arenas: invalid bool "maybe"`},
		{"optimize_for", "FAST", `file option optimize_for: unknown value "FAST" for google.protobuf.FileOptions.OptimizeMode`}, //nolint:misspell // the option's name
		{"features", "x", "file option features is a message and cannot be set"},
		{"uninterpreted_option", "x", "file option uninterpreted_option is repeated and cannot be set"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := New(nil)
			err := s.SetFileOption(tt.name, tt.value)
			if err == nil || err.Error() != tt.want {
				t.Errorf("SetFileOption(%q, %q) = %v, want %s", tt.name, tt.value, err, tt.want)
			}
			if s.fileOptions != nil {
				t.Errorf("fileOptions = %v after an error, want nil", s.fileOptions)
			}
		})
	}
}
