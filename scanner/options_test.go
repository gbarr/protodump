package scanner

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/known/timestamppb"
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

func TestSetGoPackage(t *testing.T) {
	timestamp := protodesc.ToFileDescriptorProto(timestamppb.File_google_protobuf_timestamp_proto)
	files := []*descriptorpb.FileDescriptorProto{
		{
			Name: new("Foo/Bar/FooBar.proto"), Syntax: new("proto3"), Package: new("Foo.Bar"),
			Options:     &descriptorpb.FileOptions{GoPackage: new("example.com/original")},
			MessageType: []*descriptorpb.DescriptorProto{message("Msg", 1)},
		},
		{
			Name: new("api/user.proto"), Syntax: new("proto3"), Package: new("api"),
			Dependency: []string{"Foo/Bar/FooBar.proto", "google/protobuf/timestamp.proto", "missing.proto"},
			MessageType: []*descriptorpb.DescriptorProto{{
				Name: new("User"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{
						Name: new("msg"), Number: new(int32(1)), JsonName: new("msg"),
						Label:    new(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL),
						Type:     new(descriptorpb.FieldDescriptorProto_TYPE_MESSAGE),
						TypeName: new(".Foo.Bar.Msg"),
					},
					{
						Name: new("at"), Number: new(int32(2)), JsonName: new("at"),
						Label:    new(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL),
						Type:     new(descriptorpb.FieldDescriptorProto_TYPE_MESSAGE),
						TypeName: new(".google.protobuf.Timestamp"),
					},
				},
			}},
		},
		{Name: new("dir/nopackage.proto"), Syntax: new("proto3")},
		timestamp,
	}
	s := New(nil)
	if err := s.ScanFile(writeDescriptors(t, files...)); err != nil {
		t.Fatalf("ScanFile: %v", err)
	}
	// SetGoPackage replaces a go_package set by SetFileOption, whichever is
	// called first.
	if err := s.SetFileOption("go_package", "example.com/ignored"); err != nil {
		t.Fatal(err)
	}
	s.SetGoPackage("example.com/proto/")

	out := t.TempDir()
	// All but timestamp.proto, a well-known file.
	if n, err := s.WriteFiles(out); err != nil || n != len(files)-1 {
		t.Fatalf("WriteFiles = %d, %v, want %d, nil", n, err, len(files)-1)
	}

	want := map[string]string{
		"foo_barpb/FooBar.proto": `syntax = "proto3";

package Foo.Bar;

option go_package = "example.com/proto/foo_barpb";

message Msg {
  int32 a = 1;
}
`,
		"apipb/user.proto": `syntax = "proto3";

package api;

import "foo_barpb/FooBar.proto";
import "google/protobuf/timestamp.proto";
import "missing.proto";

option go_package = "example.com/proto/apipb";

message User {
  .Foo.Bar.Msg msg = 1;
  .google.protobuf.Timestamp at = 2;
}
`,
		"nopackage.proto": `syntax = "proto3";

option go_package = "example.com/proto";
`,
	}
	for name, want := range want {
		got, err := os.ReadFile(filepath.Join(out, filepath.FromSlash(name)))
		if err != nil {
			t.Error(err)
			continue
		}
		if string(got) != want {
			t.Errorf("%s:\n%s\nwant:\n%s", name, got, want)
		}
	}

}

func TestSetGoPackageConflict(t *testing.T) {
	files := []*descriptorpb.FileDescriptorProto{
		{Name: new("b/x.proto"), Syntax: new("proto3"), Package: new("p")},
		{Name: new("a/x.proto"), Syntax: new("proto3"), Package: new("p")},
		{
			Name: new("c/y.proto"), Syntax: new("proto3"), Package: new("q"),
			Dependency: []string{"b/x.proto"},
		},
	}
	s := New(nil)
	if err := s.ScanFile(writeDescriptors(t, files...)); err != nil {
		t.Fatalf("ScanFile: %v", err)
	}
	s.SetGoPackage("example.com/proto")

	var buf strings.Builder
	err := s.Dump(&buf)
	errs, ok := errors.AsType[Errors](err)
	if !ok || len(errs) != 1 {
		t.Fatalf("Dump error = %v, want Errors with 1 error", err)
	}
	if want := "layout b/x.proto: ppb/x.proto is already used by a/x.proto"; errs[0].Error() != want {
		t.Errorf("errs[0] = %q, want %q", errs[0], want)
	}
	want := `// ppb/x.proto
syntax = "proto3";

package p;

option go_package = "example.com/proto/ppb";

// qpb/y.proto
syntax = "proto3";

package q;

import "b/x.proto";

option go_package = "example.com/proto/qpb";
`
	if got := buf.String(); got != want {
		t.Errorf("Dump output:\n%s\nwant:\n%s", got, want)
	}
}
