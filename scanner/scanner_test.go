package scanner

import (
	"bytes"
	"errors"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
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

func descriptor(t *testing.T, name string, prefix, suffix []byte) []byte {
	t.Helper()
	file := &descriptorpb.DescriptorProto{Name: new(name)}
	encoded, err := proto.Marshal(file)
	if err != nil {
		t.Fatal(err)
	}
	return slices.Concat(bytes.Clone(prefix), encoded, suffix)
}

// randomBytes returns n random bytes. None of them is magicByte or '.', so
// that they cannot form a tag byte or a protoExtn of their own. If tagLast is
// true, the last byte is set to magicByte.
func randomBytes(r *rand.Rand, n int, tagLast bool) []byte {
	b := make([]byte, n)
	for i := range b {
		for {
			c := byte(r.UintN(256))
			if c != magicByte && c != '.' {
				b[i] = c
				break
			}
		}
	}
	if tagLast && n > 0 {
		b[n-1] = magicByte
	}
	return b
}

func TestFindDescriptor(t *testing.T) {
	// A fixed seed keeps failures reproducible.
	r := rand.New(rand.NewPCG(1, 2))
	random := func(n int) []byte { return randomBytes(r, n, false) }
	ext := []byte(protoExtn)
	tag := []byte{magicByte}

	tests := []struct {
		desc   string
		prefix []byte
		suffix []byte
		name   string
		want   int
	}{
		{"no prefix or suffix", nil, nil, "a.proto", 0},
		{"random prefix", randomBytes(r, 16, false), nil, "a.proto", 16},
		{"random suffix", nil, randomBytes(r, 16, false), "a.proto", 0},
		{"random prefix and suffix", randomBytes(r, 7, false), randomBytes(r, 9, false), "dir/file.proto", 7},
		{"prefix ends in tag", randomBytes(r, 16, true), randomBytes(r, 8, false), "a.proto", 16},
		{"prefix is only tag", randomBytes(r, 1, true), nil, "a.proto", 1},

		// Name lengths of 9 and 10 either side of magicByte, where the
		// length byte can be mistaken for the tag.
		{"name length 9", randomBytes(r, 12, false), randomBytes(r, 12, false), "abc.proto", 12},
		{"name length 9, prefix ends in tag", randomBytes(r, 12, true), randomBytes(r, 12, false), "abc.proto", 12},
		{"name length 10", randomBytes(r, 12, false), randomBytes(r, 12, false), "user.proto", 12},
		{"name length 10, prefix ends in tag", randomBytes(r, 12, true), randomBytes(r, 12, false), "user.proto", 12},
		{"name length 10, no prefix", nil, randomBytes(r, 12, false), "user.proto", 0},
		{"name length 10, prefix is only tag", randomBytes(r, 1, true), nil, "user.proto", 1},
		{"name length 11", randomBytes(r, 12, false), randomBytes(r, 12, false), "users.proto", 12},
		{"name length 11, prefix ends in tag", randomBytes(r, 12, true), randomBytes(r, 12, false), "users.proto", 12},

		// Names that do not end in protoExtn are never found.
		{"not proto", randomBytes(r, 12, false), randomBytes(r, 12, false), "a.txt", -1},
		{"not proto, prefix ends in tag", randomBytes(r, 12, true), randomBytes(r, 12, false), "a.txt", -1},
		{"not proto, name length 9", randomBytes(r, 12, false), randomBytes(r, 12, false), "abc.protx", -1},
		{"not proto, name length 9, prefix ends in tag", randomBytes(r, 12, true), randomBytes(r, 12, false), "abc.protx", -1},
		{"not proto, name length 10", randomBytes(r, 12, false), randomBytes(r, 12, false), "user.protx", -1},
		{"not proto, name length 10, prefix ends in tag", randomBytes(r, 12, true), randomBytes(r, 12, false), "user.protx", -1},
		{"proto not at end of name", randomBytes(r, 12, false), randomBytes(r, 12, false), "a.proto.bak", -1},
		{"empty name", randomBytes(r, 12, true), randomBytes(r, 12, false), "", -1},

		// Prefixes containing protoExtn that is not part of a valid
		// filename field, which must be skipped over.
		{"prefix has proto with no tag", slices.Concat(random(8), ext, random(8)), nil, "a.proto", 22},
		{"prefix has proto with tag", slices.Concat(random(4), tag, random(4), ext, random(8)), nil, "a.proto", 23},
		{"prefix has proto, length too long", slices.Concat(random(4), tag, []byte{0x20}, []byte("abc"), ext, random(4)), nil, "a.proto", 19},
		{"prefix has proto, length too short", slices.Concat(random(4), tag, []byte{0x02}, []byte("abc"), ext, random(4)), nil, "a.proto", 19},
		{"prefix has proto at end", slices.Concat(random(8), ext), nil, "a.proto", 14},
		{"prefix has proto then tag", slices.Concat(random(8), ext, random(4), tag), randomBytes(r, 8, false), "a.proto", 19},
		{"prefix has proto twice", slices.Concat(random(4), ext, random(4), tag, random(2), ext, random(4)), randomBytes(r, 8, false), "a.proto", 27},
		{"prefix has proto, name length 10", slices.Concat(random(4), tag, random(4), ext, random(4)), randomBytes(r, 8, false), "user.proto", 19},
		{"prefix has proto, name length 10, prefix ends in tag", slices.Concat(random(4), tag, random(4), ext, random(4), tag), randomBytes(r, 8, false), "user.proto", 20},
		{"prefix has proto, not proto", slices.Concat(random(4), tag, random(4), ext, random(4)), randomBytes(r, 8, false), "a.txt", -1},
		{"prefix has proto, not proto, prefix ends in tag", slices.Concat(random(8), ext, random(4), tag), randomBytes(r, 8, false), "user.protx", -1},
	}
	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			if strings.HasSuffix(tt.name, protoExtn) != (tt.want != -1) {
				t.Fatalf("bad test: name %q with want %d", tt.name, tt.want)
			}
			data := descriptor(t, tt.name, tt.prefix, tt.suffix)
			if got := findDescriptor(data); got != tt.want {
				t.Errorf("findDescriptor(% x) = %d, want %d", data, got, tt.want)
			}
		})
	}
}

func TestExtractDescriptor(t *testing.T) {
	encoded, err := proto.Marshal(&descriptorpb.FileDescriptorProto{
		Name:        new("a.proto"),
		Syntax:      new("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{message("A", 1, 2)},
	})
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		desc   string
		suffix []byte
		want   []byte
	}{
		{"end of data", nil, encoded},
		{"null terminated", []byte{0, 'x'}, encoded},
		// 'g' (0x67) is wire type 7, which is invalid, as is '/' (0x2f).
		{"followed by string", []byte("github.com/x/y"), encoded},
		{"followed by path", []byte("/usr/lib"), encoded},
		{"followed by varint tag 0", []byte{0x80, 0x00}, encoded},
		{"followed by truncated varint", []byte{0x08, 0x80}, encoded},
		{"followed by truncated bytes field", []byte{0x12, 0x20, 'a', 'b'}, encoded},
		{"followed by length past end of data", []byte{0x12, 0xff, 0xff, 0x03}, encoded},
	}
	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			data := slices.Concat(encoded, tt.suffix)
			if got := extractDescriptor(data); !bytes.Equal(got, tt.want) {
				t.Errorf("extractDescriptor(% x) = % x, want % x", data, got, tt.want)
			}
		})
	}

	for _, data := range [][]byte{nil, {}, {0}, []byte("github"), {0x12, 0x05, 'a'}} {
		if got := extractDescriptor(data); len(got) != 0 {
			t.Errorf("extractDescriptor(% x) = % x, want empty", data, got)
		}
	}
}

// TestScanFileUnterminated checks that descriptors not followed by a null
// byte, such as Go rawDesc strings packed among other string data, are
// still found.
func TestScanFileUnterminated(t *testing.T) {
	files := []*descriptorpb.FileDescriptorProto{
		{Name: new("a.proto"), Syntax: new("proto3"), MessageType: []*descriptorpb.DescriptorProto{message("A", 1)}},
		{Name: new("b.proto"), Syntax: new("proto3"), MessageType: []*descriptorpb.DescriptorProto{message("B", 1, 2)}},
	}
	encode := func(i int) []byte {
		encoded, err := proto.Marshal(files[i])
		if err != nil {
			t.Fatal(err)
		}
		return encoded
	}

	tests := []struct {
		desc string
		data []byte
	}{
		{"followed by string", slices.Concat([]byte("filler"), encode(0), []byte("github.com/x"), encode(1), []byte("/usr/lib"))},
		{"back to back with end of data", slices.Concat([]byte("filler"), encode(0), []byte("github.com/x"), encode(1))},
		{"followed by truncated field", slices.Concat([]byte("filler"), encode(0), []byte{0x12, 0x7f, 'x'}, encode(1), []byte{0x08})},
	}
	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "binary")
			if err := os.WriteFile(path, tt.data, 0o755); err != nil {
				t.Fatal(err)
			}
			s := New(nil)
			if err := s.ScanFile(path); err != nil {
				t.Fatalf("ScanFile error = %v, want nil", err)
			}
			for _, file := range files {
				fd, err := s.Files().FindFileByPath(file.GetName())
				if err != nil {
					t.Errorf("%s not registered: %v", file.GetName(), err)
					continue
				}
				if got, want := fd.Messages().Get(0).Fields().Len(), len(file.MessageType[0].Field); got != want {
					t.Errorf("%s has %d fields, want %d", file.GetName(), got, want)
				}
			}
		})
	}
}

// TestExtractDescriptorFields checks that extractDescriptor stops at the
// first field that cannot belong to a FileDescriptorProto: an unknown field
// number, the wrong wire type, or a field number lower than the one before.
func TestExtractDescriptorFields(t *testing.T) {
	str := func(num protowire.Number, s string) []byte {
		return protowire.AppendString(protowire.AppendTag(nil, num, protowire.BytesType), s)
	}
	varint := func(num protowire.Number, v uint64) []byte {
		return protowire.AppendVarint(protowire.AppendTag(nil, num, protowire.VarintType), v)
	}
	packed := func(num protowire.Number, vs ...uint64) []byte {
		var b []byte
		for _, v := range vs {
			b = protowire.AppendVarint(b, v)
		}
		return protowire.AppendBytes(protowire.AppendTag(nil, num, protowire.BytesType), b)
	}
	fixed32 := func(num protowire.Number, v uint32) []byte {
		return protowire.AppendFixed32(protowire.AppendTag(nil, num, protowire.Fixed32Type), v)
	}

	name := str(1, "a.proto")
	pkg := str(2, "pkg")
	syntax := str(12, "proto3")

	tests := []struct {
		desc   string
		fields [][]byte // the descriptor's fields
		extra  [][]byte // trailing bytes that must not be included
	}{
		{"all valid", [][]byte{name, pkg, str(3, "b.proto"), str(3, "c.proto"), varint(10, 0), varint(11, 1), syntax, varint(14, 1000)}, nil},
		{"repeated field", [][]byte{name, str(3, "b.proto"), str(3, "c.proto"), str(3, "d.proto")}, nil},
		{"packed public_dependency", [][]byte{name, str(3, "b.proto"), str(3, "c.proto"), packed(10, 0, 1)}, nil},
		{"packed weak_dependency", [][]byte{name, str(3, "b.proto"), packed(11, 0)}, nil},

		// Field numbers going backwards, which protoc never writes.
		{"name after syntax", [][]byte{name, syntax}, [][]byte{str(1, "x.proto")}},
		{"name after package", [][]byte{name, pkg}, [][]byte{str(1, "x.proto"), syntax}},
		{"package after syntax", [][]byte{name, syntax}, [][]byte{pkg}},
		{"name after edition", [][]byte{name, varint(14, 1000)}, [][]byte{str(1, "x.proto")}},

		// Field numbers that FileDescriptorProto does not have.
		{"field 13", [][]byte{name, syntax}, [][]byte{str(13, "x")}},
		{"field 16", [][]byte{name, syntax}, [][]byte{str(16, "x")}},
		{"field 1000", [][]byte{name, syntax}, [][]byte{varint(1000, 1)}},

		// Known field numbers with the wrong wire type.
		{"syntax as varint", [][]byte{name}, [][]byte{varint(12, 3)}},
		{"package as fixed32", [][]byte{name}, [][]byte{fixed32(2, 1)}},
		{"edition as fixed32", [][]byte{name, syntax}, [][]byte{fixed32(14, 1000)}},
		{"edition packed", [][]byte{name, syntax}, [][]byte{packed(14, 1000)}},
		{"public_dependency as fixed32", [][]byte{name}, [][]byte{fixed32(10, 0)}},
	}
	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			want := slices.Concat(tt.fields...)
			data := slices.Concat(want, slices.Concat(tt.extra...))
			if got := extractDescriptor(data); !bytes.Equal(got, want) {
				t.Errorf("extractDescriptor(% x)\n = % x\nwant % x", data, got, want)
			}
		})
	}
}

// TestScanFileTrailingName checks that a name field straight after a
// descriptor does not replace the descriptor's own name.
func TestScanFileTrailingName(t *testing.T) {
	encoded, err := proto.Marshal(&descriptorpb.FileDescriptorProto{
		Name:        new("a.proto"),
		Syntax:      new("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{message("A", 1)},
	})
	if err != nil {
		t.Fatal(err)
	}
	trailing := protowire.AppendString(protowire.AppendTag(nil, 1, protowire.BytesType), "x.proto")
	path := filepath.Join(t.TempDir(), "binary")
	if err := os.WriteFile(path, slices.Concat([]byte("filler"), encoded, trailing, []byte("/usr/lib")), 0o755); err != nil {
		t.Fatal(err)
	}

	s := New(nil)
	if err := s.ScanFile(path); err != nil {
		t.Fatalf("ScanFile error = %v, want nil", err)
	}
	fd, err := s.Files().FindFileByPath("a.proto")
	if err != nil {
		t.Fatalf("a.proto not registered: %v", err)
	}
	if fd.Messages().ByName("A") == nil {
		t.Errorf("a.proto has no message A")
	}
}
