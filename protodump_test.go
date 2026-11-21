package protodump

import (
	"bytes"
	"errors"
	"flag"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

var update = flag.Bool("update", false, "regenerate "+descriptorSetFile+" with protoc instead of verifying it")

// descriptorSetFile holds protoc's compiled descriptors for the testdata
// .proto files, in text format, so the tests can run without protoc.
const descriptorSetFile = "testdata/descriptors.txtpb"

// TestDumpTestdata checks that Dump recreates every .proto file under
// testdata exactly from its compiled descriptor.
//
// The files are written in Dump's output format, so they double as golden
// files: protoc checks that they are valid, and an exact match means the dump
// describes the same thing as the input.
//
// The descriptors come from protoc when it is installed, and are checked
// against descriptorSetFile; run "go test -update" to regenerate that file
// after changing the testdata. Without protoc, descriptorSetFile is used.
func TestDumpTestdata(t *testing.T) {
	var paths []string
	err := filepath.WalkDir("testdata", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".proto") {
			rel, err := filepath.Rel("testdata", path)
			if err != nil {
				return err
			}
			paths = append(paths, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("listing testdata: %v", err)
	}
	slices.Sort(paths)

	set := testdataDescriptors(t, paths)
	files, err := protodesc.NewFiles(set)
	if err != nil {
		t.Fatalf("protodesc.NewFiles: %v", err)
	}
	protos := map[string]*descriptorpb.FileDescriptorProto{}
	for _, file := range set.File {
		protos[file.GetName()] = file
	}

	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			want, err := os.ReadFile(filepath.Join("testdata", filepath.FromSlash(path)))
			if err != nil {
				t.Fatal(err)
			}
			pb, ok := protos[path]
			if !ok {
				t.Fatalf("%s not found in descriptors (is %s out of date? run go test -update)", path, descriptorSetFile)
			}

			var buf bytes.Buffer
			if err := Dump(&buf, pb, files); err != nil {
				t.Fatalf("Dump: %v", err)
			}
			if diff := cmp.Diff(string(want), buf.String()); diff != "" {
				t.Errorf("Dump output differs from testdata/%s (-want +got):\n%s", path, diff)
			}
		})
	}
}

// testdataDescriptors returns the compiled descriptors for paths. With protoc
// installed it compiles them and either rewrites descriptorSetFile (with
// -update) or checks that the file matches. Without protoc it reads the file.
func testdataDescriptors(t *testing.T, paths []string) *descriptorpb.FileDescriptorSet {
	t.Helper()

	protoc, err := exec.LookPath("protoc")
	if err != nil {
		if *update {
			t.Fatalf("-update requires protoc: %v", err)
		}
		t.Logf("protoc not found; using %s", descriptorSetFile)
		return readDescriptorSet(t)
	}

	out := filepath.Join(t.TempDir(), "out.pb")
	args := append([]string{"-I", "testdata", "--include_imports", "--descriptor_set_out=" + out}, paths...)
	if msg, err := exec.Command(protoc, args...).CombinedOutput(); err != nil {
		t.Fatalf("protoc: %v\n%s", err, msg)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("reading protoc output: %v", err)
	}
	compiled := new(descriptorpb.FileDescriptorSet)
	if err := proto.Unmarshal(raw, compiled); err != nil {
		t.Fatalf("unmarshal protoc output: %v", err)
	}

	if *update {
		text, err := prototext.MarshalOptions{Multiline: true}.Marshal(compiled)
		if err != nil {
			t.Fatalf("marshal %s: %v", descriptorSetFile, err)
		}
		if err := os.WriteFile(descriptorSetFile, text, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("updated %s", descriptorSetFile)
		return compiled
	}

	if saved := readDescriptorSet(t); !proto.Equal(saved, compiled) {
		t.Errorf("%s does not match protoc output; run go test -update", descriptorSetFile)
	}
	return compiled
}

func readDescriptorSet(t *testing.T) *descriptorpb.FileDescriptorSet {
	t.Helper()
	text, err := os.ReadFile(descriptorSetFile)
	if err != nil {
		t.Fatalf("%v (run go test -update with protoc installed to create it)", err)
	}
	set := new(descriptorpb.FileDescriptorSet)
	if err := prototext.Unmarshal(text, set); err != nil {
		t.Fatalf("parse %s: %v", descriptorSetFile, err)
	}
	return set
}

// The tests below cover paths that protoc-compiled testdata cannot reach.

func TestEditionString(t *testing.T) {
	tests := []struct {
		edition descriptorpb.Edition
		want    string
		wantErr bool
	}{
		{edition: descriptorpb.Edition_EDITION_2023, want: "2023"},
		{edition: descriptorpb.Edition_EDITION_2024, want: "2024"},
		{edition: descriptorpb.Edition_EDITION_PROTO3, wantErr: true},
		{edition: descriptorpb.Edition_EDITION_1_TEST_ONLY, wantErr: true},
		{edition: descriptorpb.Edition_EDITION_MAX, wantErr: true},
		{edition: descriptorpb.Edition_EDITION_UNKNOWN, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.edition.String(), func(t *testing.T) {
			got, err := editionString(tt.edition)
			if (err != nil) != tt.wantErr {
				t.Fatalf("editionString(%v) error = %v, wantErr %v", tt.edition, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("editionString(%v) = %q, want %q", tt.edition, got, tt.want)
			}
		})
	}
}

// TestDumpInvalidDescriptor checks that Dump reports a descriptor it
// cannot build, here one using an edition the Go runtime does not support.
func TestDumpInvalidDescriptor(t *testing.T) {
	pb := &descriptorpb.FileDescriptorProto{
		Name:    new("a.proto"),
		Syntax:  new("editions"),
		Edition: new(descriptorpb.Edition_EDITION_MAX),
	}
	var buf bytes.Buffer
	err := Dump(&buf, pb, nil)
	if err == nil || !strings.Contains(err.Error(), "building descriptor for a.proto") {
		t.Errorf("Dump error = %v, want building descriptor for a.proto", err)
	}
}

func TestOptionAssignmentsNil(t *testing.T) {
	if got := optionAssignments(nil); got != nil {
		t.Errorf("optionAssignments(nil) = %q, want nil", got)
	}
	if got := optionAssignments((*descriptorpb.FieldOptions)(nil)); got != nil {
		t.Errorf("optionAssignments(typed nil) = %q, want nil", got)
	}
}

// TestDumpCustomOptions covers custom options that are known extension
// fields. protoc output cannot be used here: parsing it without the option
// definitions leaves custom options as unknown fields, which are not written.
func TestDumpCustomOptions(t *testing.T) {
	scalar := func(name string, number int32, typ descriptorpb.FieldDescriptorProto_Type) *descriptorpb.FieldDescriptorProto {
		return &descriptorpb.FieldDescriptorProto{
			Name:   new(name),
			Number: new(number),
			Label:  new(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL),
			Type:   new(typ),
		}
	}
	ext := func(f *descriptorpb.FieldDescriptorProto, extendee, typeName string, label descriptorpb.FieldDescriptorProto_Label) *descriptorpb.FieldDescriptorProto {
		f.Extendee = new(extendee)
		f.Label = new(label)
		if typeName != "" {
			f.TypeName = new(typeName)
		}
		return f
	}
	optional := descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL
	repeated := descriptorpb.FieldDescriptorProto_LABEL_REPEATED

	// syntax = "proto2";
	// package opts;
	// import "google/protobuf/descriptor.proto";
	// enum Colour { RED = 0; }
	// message Info {
	//   optional string name = 1;
	//   repeated int32 ids = 2;
	//   optional group Sub = 3 { optional int32 v = 1; }
	//   extensions 100 to 199;
	// }
	// extend Info { optional int32 extra = 100; }
	// extend google.protobuf.FieldOptions {
	//   optional int32 num = 50000;
	//   optional bytes data = 50001;
	//   optional double ratio = 50002;
	//   optional Colour colour = 50003;
	//   repeated Info items = 50004;
	//   optional Info info = 50005;
	// }
	const fieldOptions = ".google.protobuf.FieldOptions"
	group := scalar("sub", 3, descriptorpb.FieldDescriptorProto_TYPE_GROUP)
	group.TypeName = new(".opts.Info.Sub")
	ids := scalar("ids", 2, descriptorpb.FieldDescriptorProto_TYPE_INT32)
	ids.Label = new(repeated)
	optsFile, err := protodesc.NewFile(&descriptorpb.FileDescriptorProto{
		Name:       new("opts.proto"),
		Syntax:     new("proto2"),
		Package:    new("opts"),
		Dependency: []string{"google/protobuf/descriptor.proto"},
		EnumType: []*descriptorpb.EnumDescriptorProto{{
			Name:  new("Colour"),
			Value: []*descriptorpb.EnumValueDescriptorProto{{Name: new("RED"), Number: new(int32(0))}},
		}},
		MessageType: []*descriptorpb.DescriptorProto{{
			Name:           new("Info"),
			Field:          []*descriptorpb.FieldDescriptorProto{scalar("name", 1, descriptorpb.FieldDescriptorProto_TYPE_STRING), ids, group},
			NestedType:     []*descriptorpb.DescriptorProto{{Name: new("Sub"), Field: []*descriptorpb.FieldDescriptorProto{scalar("v", 1, descriptorpb.FieldDescriptorProto_TYPE_INT32)}}},
			ExtensionRange: []*descriptorpb.DescriptorProto_ExtensionRange{{Start: new(int32(100)), End: new(int32(200))}},
		}},
		Extension: []*descriptorpb.FieldDescriptorProto{
			ext(scalar("extra", 100, descriptorpb.FieldDescriptorProto_TYPE_INT32), ".opts.Info", "", optional),
			ext(scalar("num", 50000, descriptorpb.FieldDescriptorProto_TYPE_INT32), fieldOptions, "", optional),
			ext(scalar("data", 50001, descriptorpb.FieldDescriptorProto_TYPE_BYTES), fieldOptions, "", optional),
			ext(scalar("ratio", 50002, descriptorpb.FieldDescriptorProto_TYPE_DOUBLE), fieldOptions, "", optional),
			ext(scalar("colour", 50003, descriptorpb.FieldDescriptorProto_TYPE_ENUM), fieldOptions, ".opts.Colour", optional),
			ext(scalar("items", 50004, descriptorpb.FieldDescriptorProto_TYPE_MESSAGE), fieldOptions, ".opts.Info", repeated),
			ext(scalar("info", 50005, descriptorpb.FieldDescriptorProto_TYPE_MESSAGE), fieldOptions, ".opts.Info", optional),
		},
	}, protoregistry.GlobalFiles)
	if err != nil {
		t.Fatalf("protodesc.NewFile(opts.proto): %v", err)
	}
	optsExt := func(name protoreflect.Name) protoreflect.ExtensionDescriptor {
		xd := optsFile.Extensions().ByName(name)
		if xd == nil {
			t.Fatalf("no extension %s", name)
		}
		return dynamicpb.NewExtensionType(xd).TypeDescriptor()
	}
	info := func(set func(m protoreflect.Message)) protoreflect.Value {
		m := dynamicpb.NewMessage(optsFile.Messages().ByName("Info"))
		set(m)
		return protoreflect.ValueOfMessage(m)
	}
	infoField := func(m protoreflect.Message, name protoreflect.Name) protoreflect.FieldDescriptor {
		return m.Descriptor().Fields().ByName(name)
	}

	opts := (&descriptorpb.FieldOptions{Deprecated: new(true)}).ProtoReflect()
	opts.Set(optsExt("num"), protoreflect.ValueOfInt32(5))
	opts.Set(optsExt("data"), protoreflect.ValueOfBytes([]byte("\x01b")))
	opts.Set(optsExt("ratio"), protoreflect.ValueOfFloat64(math.Inf(1)))
	// An enum number with no value: written as the number.
	opts.Set(optsExt("colour"), protoreflect.ValueOfEnum(7))
	opts.Set(optsExt("info"), info(func(m protoreflect.Message) {
		m.Set(infoField(m, "name"), protoreflect.ValueOfString("solo"))
	}))
	items := opts.NewField(optsExt("items")).List()
	items.Append(info(func(m protoreflect.Message) {
		m.Set(infoField(m, "name"), protoreflect.ValueOfString("a"))
		list := m.NewField(infoField(m, "ids")).List()
		list.Append(protoreflect.ValueOfInt32(1))
		list.Append(protoreflect.ValueOfInt32(2))
		m.Set(infoField(m, "ids"), protoreflect.ValueOfList(list))
	}))
	items.Append(info(func(m protoreflect.Message) {
		m.Set(infoField(m, "name"), protoreflect.ValueOfString("b"))
		sub := m.NewField(infoField(m, "sub")).Message()
		sub.Set(sub.Descriptor().Fields().ByName("v"), protoreflect.ValueOfInt32(3))
		m.Set(infoField(m, "sub"), protoreflect.ValueOfMessage(sub))
		m.Set(dynamicpb.NewExtensionType(optsFile.Extensions().ByName("extra")).TypeDescriptor(), protoreflect.ValueOfInt32(9))
	}))
	// An empty element is written as {}.
	items.Append(info(func(protoreflect.Message) {}))
	opts.Set(optsExt("items"), protoreflect.ValueOfList(items))

	var files protoregistry.Files
	for _, f := range []protoreflect.FileDescriptor{descriptorpb.File_google_protobuf_descriptor_proto, optsFile} {
		if err := files.RegisterFile(f); err != nil {
			t.Fatalf("RegisterFile(%s): %v", f.Path(), err)
		}
	}
	pb := &descriptorpb.FileDescriptorProto{
		Name:       new("a.proto"),
		Syntax:     new("proto2"),
		Dependency: []string{"opts.proto"},
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: new("M"),
			Field: []*descriptorpb.FieldDescriptorProto{{
				Name:    new("f"),
				Number:  new(int32(1)),
				Label:   new(optional),
				Type:    new(descriptorpb.FieldDescriptorProto_TYPE_INT32),
				Options: opts.Interface().(*descriptorpb.FieldOptions),
			}},
		}},
	}
	var buf bytes.Buffer
	if err := Dump(&buf, pb, &files); err != nil {
		t.Fatalf("Dump: %v", err)
	}

	// Standard options come first, then custom options in name order.
	want := `syntax = "proto2";

import "opts.proto";

message M {
  optional int32 f = 1 [` + strings.Join([]string{
		`deprecated = true`,
		`(opts.colour) = 7`,
		`(opts.data) = "\001b"`,
		`(opts.info).name = "solo"`,
		`(opts.items) = { name: "a" ids: 1 ids: 2 }`,
		`(opts.items) = { name: "b" Sub: { v: 3 } [opts.extra]: 9 }`,
		`(opts.items) = {}`,
		`(opts.num) = 5`,
		`(opts.ratio) = inf`,
	}, ", ") + `];
}
`
	if diff := cmp.Diff(want, buf.String()); diff != "" {
		t.Errorf("Dump output (-want +got):\n%s", diff)
	}
}

// TestDumpUnresolvedTypeRef covers a reference to a type that could not be
// resolved. Its placeholder descriptor has no parent file, so the package is
// unknown and the reference must be fully qualified.
func TestDumpUnresolvedTypeRef(t *testing.T) {
	pb := &descriptorpb.FileDescriptorProto{
		Name:    new("a.proto"),
		Syntax:  new("proto3"),
		Package: new("p"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: new("M"),
			Field: []*descriptorpb.FieldDescriptorProto{{
				Name:     new("missing"),
				Number:   new(int32(1)),
				Label:    new(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL),
				Type:     new(descriptorpb.FieldDescriptorProto_TYPE_MESSAGE),
				TypeName: new(".p.Missing"),
			}},
		}},
	}
	fd, err := protodesc.FileOptions{AllowUnresolvable: true}.New(pb, nil)
	if err != nil {
		t.Fatalf("protodesc.New: %v", err)
	}
	if target := fd.Messages().Get(0).Fields().Get(0).Message(); target.ParentFile() != nil {
		t.Fatalf("placeholder %s has a parent file; test does not cover the nil case", target.FullName())
	}

	var buf bytes.Buffer
	if err := Dump(&buf, pb, nil); err != nil {
		t.Fatalf("Dump: %v", err)
	}
	if want := "\n  .p.Missing missing = 1;\n"; !strings.Contains(buf.String(), want) {
		t.Errorf("output missing %q\n%s", want, buf.String())
	}
}

// noParentFile wraps a field descriptor so that ParentFile returns nil.
type noParentFile struct {
	protoreflect.FieldDescriptor
}

func (noParentFile) ParentFile() protoreflect.FileDescriptor { return nil }

func TestFieldHelpersWithoutParentFile(t *testing.T) {
	field := func(name string, number int32, label descriptorpb.FieldDescriptorProto_Label, typ descriptorpb.FieldDescriptorProto_Type) *descriptorpb.FieldDescriptorProto {
		return &descriptorpb.FieldDescriptorProto{Name: new(name), Number: new(number), Label: new(label), Type: new(typ)}
	}
	group := field("g", 2, descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL, descriptorpb.FieldDescriptorProto_TYPE_GROUP)
	group.TypeName = new(".M.G")
	fd, err := protodesc.NewFile(&descriptorpb.FileDescriptorProto{
		Name:   new("a.proto"),
		Syntax: new("proto2"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name:       new("M"),
			Field:      []*descriptorpb.FieldDescriptorProto{field("r", 1, descriptorpb.FieldDescriptorProto_LABEL_REQUIRED, descriptorpb.FieldDescriptorProto_TYPE_INT32), group},
			NestedType: []*descriptorpb.DescriptorProto{{Name: new("G")}},
		}},
	}, nil)
	if err != nil {
		t.Fatalf("protodesc.NewFile: %v", err)
	}
	fields := fd.Messages().Get(0).Fields()
	required, grp := fields.ByName("r"), fields.ByName("g")

	// With a parent file these are proto2-specific.
	if got := fieldCardinality(required); got != "required " {
		t.Fatalf("fieldCardinality(required) = %q, want %q", got, "required ")
	}
	if !isProto2Group(grp) {
		t.Fatal("isProto2Group(group) = false, want true")
	}

	// Without one, the syntax is unknown and no syntax-specific case applies.
	if got := fileSyntax(noParentFile{required}); got != 0 {
		t.Errorf("fileSyntax without parent file = %v, want 0", got)
	}
	if got := fieldCardinality(noParentFile{required}); got != "" {
		t.Errorf("fieldCardinality(required) without parent file = %q, want \"\"", got)
	}
	if isProto2Group(noParentFile{grp}) {
		t.Error("isProto2Group(group) without parent file = true, want false")
	}
}

type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

func TestDumpWriteError(t *testing.T) {
	pb := &descriptorpb.FileDescriptorProto{Name: new("a.proto"), Syntax: new("proto3")}
	want := errors.New("disk full")
	if err := Dump(failingWriter{want}, pb, nil); !errors.Is(err, want) {
		t.Errorf("Dump error = %v, want %v", err, want)
	}
}
