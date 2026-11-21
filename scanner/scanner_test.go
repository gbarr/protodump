package scanner

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

func TestErrors(t *testing.T) {
	errA, errB := errors.New("a failed"), errors.New("b failed")
	var err error = Errors{errA, errB}

	if got, want := err.Error(), "a failed\nb failed"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	if !errors.Is(err, errA) || !errors.Is(err, errB) {
		t.Error("errors.Is does not find the wrapped errors")
	}
	var errs Errors
	if !errors.As(err, &errs) || len(errs) != 2 {
		t.Errorf("errors.As = %v, want the 2 errors", errs)
	}
}

// writeDescriptors writes a file containing each descriptor, encoded and
// null-terminated as in a compiled binary, separated by filler bytes.
func writeDescriptors(t *testing.T, files ...*descriptorpb.FileDescriptorProto) string {
	t.Helper()
	data := []byte("filler")
	for _, file := range files {
		encoded, err := proto.Marshal(file)
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, encoded...)
		data = append(data, 0, 'f', 'i', 'l', 'l', 'e', 'r')
	}
	path := filepath.Join(t.TempDir(), "binary")
	if err := os.WriteFile(path, data, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func message(name string, numbers ...int32) *descriptorpb.DescriptorProto {
	m := &descriptorpb.DescriptorProto{Name: new(name)}
	for i, n := range numbers {
		m.Field = append(m.Field, &descriptorpb.FieldDescriptorProto{
			Name:   new(string(rune('a' + i))),
			Number: new(n),
			Label:  new(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL),
			Type:   new(descriptorpb.FieldDescriptorProto_TYPE_INT32),
		})
	}
	return m
}

func TestScanFile(t *testing.T) {
	good := &descriptorpb.FileDescriptorProto{
		Name:        new("good.proto"),
		Syntax:      new("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{message("Good", 1, 2)},
	}
	// Two fields with the same number cannot be registered.
	bad := &descriptorpb.FileDescriptorProto{
		Name:        new("bad.proto"),
		Syntax:      new("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{message("Bad", 1, 1)},
	}

	t.Run("no errors", func(t *testing.T) {
		s := New(nil)
		if err := s.ScanFile(writeDescriptors(t, good)); err != nil {
			t.Fatalf("ScanFile error = %v (%T), want nil", err, err)
		}
		if _, err := s.Files().FindFileByPath("good.proto"); err != nil {
			t.Errorf("good.proto not registered: %v", err)
		}
	})

	t.Run("descriptor errors", func(t *testing.T) {
		s := New(nil)
		err := s.ScanFile(writeDescriptors(t, bad, good))

		var errs Errors
		if !errors.As(err, &errs) || len(errs) != 1 {
			t.Fatalf("ScanFile error = %v, want Errors with 1 error", err)
		}
		if msg := errs[0].Error(); !strings.HasPrefix(msg, "addFileDescriptor ") || !strings.Contains(msg, `"Bad" has conflicting fields`) {
			t.Errorf("error = %q, want an addFileDescriptor error for message Bad", msg)
		}
		// The good descriptor after the bad one is still registered.
		if _, err := s.Files().FindFileByPath("good.proto"); err != nil {
			t.Errorf("good.proto not registered: %v", err)
		}
	})
}

// TestScanFileNameLengths covers file names around 10 bytes, the value of the
// name field's tag byte, where the length byte can be mistaken for the tag.
func TestScanFileNameLengths(t *testing.T) {
	for _, name := range []string{"a.proto", "abc.proto", "user.proto", "users.proto", "x/ab.proto"} {
		t.Run(name, func(t *testing.T) {
			s := New(nil)
			pb := &descriptorpb.FileDescriptorProto{Name: new(name), Syntax: new("proto3")}
			if err := s.ScanFile(writeDescriptors(t, pb)); err != nil {
				t.Fatalf("ScanFile: %v", err)
			}
			if _, err := s.Files().FindFileByPath(name); err != nil {
				t.Errorf("%s (%d bytes) not found: %v", name, len(name), err)
			}
		})
	}
}

func TestWriteFiles(t *testing.T) {
	files := []*descriptorpb.FileDescriptorProto{
		{Name: new("top.proto"), Syntax: new("proto3"), MessageType: []*descriptorpb.DescriptorProto{message("Top", 1)}},
		{Name: new("dir/nested.proto"), Syntax: new("proto3")},
	}
	s := New(nil)
	if err := s.ScanFile(writeDescriptors(t, files...)); err != nil {
		t.Fatalf("ScanFile: %v", err)
	}

	out := filepath.Join(t.TempDir(), "out")
	if err := s.WriteFiles(out); err != nil {
		t.Fatalf("WriteFiles: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(out, "top.proto"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "syntax = \"proto3\";\n\nmessage Top {\n  int32 a = 1;\n}\n"; string(got) != want {
		t.Errorf("top.proto = %q, want %q", got, want)
	}
	if _, err := os.Stat(filepath.Join(out, "dir", "nested.proto")); err != nil {
		t.Errorf("nested file not written: %v", err)
	}
}

// TestWriteFilesStaysInDir checks that a file name from a scanned descriptor
// cannot write outside the output directory.
func TestWriteFilesStaysInDir(t *testing.T) {
	s := New(nil)
	err := s.ScanFile(writeDescriptors(t, &descriptorpb.FileDescriptorProto{Name: new("../escape.proto"), Syntax: new("proto3")}))
	if err != nil {
		t.Fatalf("ScanFile: %v", err)
	}
	if _, err := s.Files().FindFileByPath("../escape.proto"); err != nil {
		t.Fatalf("../escape.proto not registered, so the test does not cover WriteFiles: %v", err)
	}

	base := t.TempDir()
	err = s.WriteFiles(filepath.Join(base, "out"))
	errs, ok := errors.AsType[Errors](err)
	if !ok || len(errs) != 1 {
		t.Fatalf("WriteFiles error = %v, want Errors with 1 error", err)
	}
	if msg := errs[0].Error(); !strings.HasPrefix(msg, "dump failed ../escape.proto: ") {
		t.Errorf("errs[0] = %q, want dump failed ../escape.proto", msg)
	}
	if _, err := os.Stat(filepath.Join(base, "escape.proto")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("escape.proto was written outside the output directory (stat err = %v)", err)
	}
}

func TestDump(t *testing.T) {
	// Registered out of order; Dump writes them sorted by path.
	files := []*descriptorpb.FileDescriptorProto{
		{Name: new("b.proto"), Syntax: new("proto3"), Package: new("b")},
		{Name: new("a.proto"), Syntax: new("proto3"), MessageType: []*descriptorpb.DescriptorProto{message("A", 1)}},
	}
	s := New(nil)
	if err := s.ScanFile(writeDescriptors(t, files...)); err != nil {
		t.Fatalf("ScanFile: %v", err)
	}

	var buf strings.Builder
	if err := s.Dump(&buf); err != nil {
		t.Fatalf("Dump: %v", err)
	}
	want := `// a.proto
syntax = "proto3";

message A {
  int32 a = 1;
}

// b.proto
syntax = "proto3";

package b;
`
	if got := buf.String(); got != want {
		t.Errorf("Dump output:\n%s\nwant:\n%s", got, want)
	}
}

type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

func TestDumpWriteError(t *testing.T) {
	s := New(nil)
	if err := s.ScanFile(writeDescriptors(t, &descriptorpb.FileDescriptorProto{Name: new("a.proto"), Syntax: new("proto3")})); err != nil {
		t.Fatalf("ScanFile: %v", err)
	}
	want := errors.New("disk full")
	if err := s.Dump(failingWriter{want}); !errors.Is(err, want) {
		t.Errorf("Dump error = %v, want %v", err, want)
	}
}
