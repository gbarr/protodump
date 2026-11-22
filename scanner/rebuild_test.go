package scanner

import (
	"bytes"
	"flag"
	"io/fs"
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
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

var update = flag.Bool("update", false, "regenerate the scanner testdata with protoc instead of verifying it")

const (
	// rebuildDir holds .proto files that import each other and use each
	// other's custom options. Each has its compiled descriptor, from protoc,
	// in a .txtpb file of the same name, so the tests can run without protoc.
	rebuildDir = "testdata/rebuild"

	// rebuildGolden holds Scanner.Dump's output for the files in rebuildDir
	// once they have been rebuilt.
	rebuildGolden = "testdata/rebuild.golden"
)

// rebuildProtos returns the paths of the .proto files under rebuildDir,
// relative to it and slash-separated as protoc names them, in sorted order.
func rebuildProtos(t *testing.T) []string {
	t.Helper()
	var paths []string
	err := filepath.WalkDir(rebuildDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, protoExtn) {
			rel, err := filepath.Rel(rebuildDir, path)
			if err != nil {
				return err
			}
			paths = append(paths, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("listing %s: %v", rebuildDir, err)
	}
	slices.Sort(paths)
	return paths
}

// txtpbPath returns the path of the .txtpb file holding the descriptor for
// name, a .proto path relative to rebuildDir.
func txtpbPath(name string) string {
	return filepath.Join(rebuildDir, filepath.FromSlash(strings.TrimSuffix(name, protoExtn)+".txtpb"))
}

// withExtensions returns a copy of each file in files with the custom
// options in it decoded as extensions, resolved from the files themselves
// together with descriptor.proto. Text format cannot hold unknown fields, so
// options must be extensions to be written as text.
func withExtensions(t *testing.T, files []*descriptorpb.FileDescriptorProto) []*descriptorpb.FileDescriptorProto {
	t.Helper()
	set := &descriptorpb.FileDescriptorSet{File: files}
	if !slices.ContainsFunc(files, func(pb *descriptorpb.FileDescriptorProto) bool {
		return pb.GetName() == descriptorProto
	}) {
		set.File = append(slices.Clone(files), protodesc.ToFileDescriptorProto(descriptorpb.File_google_protobuf_descriptor_proto))
	}
	registry, err := protodesc.NewFiles(set)
	if err != nil {
		t.Fatalf("protodesc.NewFiles: %v", err)
	}
	unmarshal := proto.UnmarshalOptions{Resolver: dynamicpb.NewTypes(registry)}

	out := make([]*descriptorpb.FileDescriptorProto, len(files))
	for i, pb := range files {
		data, err := proto.Marshal(pb)
		if err != nil {
			t.Fatalf("marshal %s: %v", pb.GetName(), err)
		}
		out[i] = new(descriptorpb.FileDescriptorProto)
		if err := unmarshal.Unmarshal(data, out[i]); err != nil {
			t.Fatalf("unmarshal %s: %v", pb.GetName(), err)
		}
	}
	return out
}

// asScanned returns pb as the scanner decodes it from a binary: with no
// extensions known, so that custom options are unknown fields.
func asScanned(t *testing.T, pb *descriptorpb.FileDescriptorProto) *descriptorpb.FileDescriptorProto {
	t.Helper()
	data, err := proto.MarshalOptions{Deterministic: true}.Marshal(pb)
	if err != nil {
		t.Fatalf("marshal %s: %v", pb.GetName(), err)
	}
	scanned := new(descriptorpb.FileDescriptorProto)
	if err := proto.Unmarshal(data, scanned); err != nil {
		t.Fatalf("unmarshal %s: %v", pb.GetName(), err)
	}
	return scanned
}

// readRebuildDescriptors reads the .txtpb file for each of paths and
// returns the descriptors as asScanned gives them, in the order of paths.
func readRebuildDescriptors(t *testing.T, paths []string) []*descriptorpb.FileDescriptorProto {
	t.Helper()

	// The custom options in the text are extensions, which can only be
	// parsed once every file is known. So parse the files first without
	// them, to build the registry that resolves them, then parse again.
	texts := make([][]byte, len(paths))
	files := make([]*descriptorpb.FileDescriptorProto, len(paths))
	for i, path := range paths {
		text, err := os.ReadFile(txtpbPath(path))
		if err != nil {
			t.Fatalf("%v (run go test -update with protoc installed to create it)", err)
		}
		texts[i] = text
		files[i] = new(descriptorpb.FileDescriptorProto)
		if err := (prototext.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(text, files[i]); err != nil {
			t.Fatalf("parse %s: %v", txtpbPath(path), err)
		}
	}
	set := &descriptorpb.FileDescriptorSet{
		File: append(slices.Clone(files), protodesc.ToFileDescriptorProto(descriptorpb.File_google_protobuf_descriptor_proto)),
	}
	registry, err := protodesc.NewFiles(set)
	if err != nil {
		t.Fatalf("protodesc.NewFiles: %v", err)
	}
	unmarshal := prototext.UnmarshalOptions{Resolver: dynamicpb.NewTypes(registry)}

	for i, path := range paths {
		pb := new(descriptorpb.FileDescriptorProto)
		if err := unmarshal.Unmarshal(texts[i], pb); err != nil {
			t.Fatalf("parse %s: %v", txtpbPath(path), err)
		}
		if pb.GetName() != path {
			t.Fatalf("%s holds the descriptor for %q, want %q", txtpbPath(path), pb.GetName(), path)
		}
		files[i] = asScanned(t, pb)
	}
	return files
}

// TestRebuildTestdataDescriptors compiles the .proto files in rebuildDir
// with protoc and checks that their .txtpb files match, or with -update
// rewrites them. It is skipped if protoc is not installed.
//
// It must come before TestRebuildDump, which reads the .txtpb files, so
// that "go test -update" regenerates them first.
func TestRebuildTestdataDescriptors(t *testing.T) {
	protoc, err := exec.LookPath("protoc")
	if err != nil {
		if *update {
			t.Fatalf("-update requires protoc: %v", err)
		}
		t.Skipf("protoc not found; using the .txtpb files in %s", rebuildDir)
	}

	paths := rebuildProtos(t)
	out := filepath.Join(t.TempDir(), "out.pb")
	// --include_imports gives descriptor.proto, to resolve custom options.
	args := append([]string{"-I", rebuildDir, "--include_imports", "--descriptor_set_out=" + out}, paths...)
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

	byName := map[string]*descriptorpb.FileDescriptorProto{}
	for _, pb := range withExtensions(t, compiled.File) {
		byName[pb.GetName()] = pb
	}

	if *update {
		for _, path := range paths {
			text, err := prototext.MarshalOptions{Multiline: true}.Marshal(byName[path])
			if err != nil {
				t.Fatalf("marshal %s: %v", path, err)
			}
			if err := os.WriteFile(txtpbPath(path), text, 0o644); err != nil {
				t.Fatal(err)
			}
			t.Logf("updated %s", txtpbPath(path))
		}
		return
	}

	saved := readRebuildDescriptors(t, paths)
	for i, path := range paths {
		if !proto.Equal(saved[i], asScanned(t, byName[path])) {
			t.Errorf("%s does not match protoc output; run go test -update", txtpbPath(path))
		}
	}
}

// TestRebuildDump registers the descriptors in rebuildDir, and
// descriptor.proto, with Scanner.register in every order, as the scanner
// would find them in a binary: without the extensions for their custom
// options, and with imports that are not registered yet. Scanner.Dump,
// which rebuilds the registry first, must give the same output for every
// order, matching rebuildGolden. With -update the golden file is rewritten
// instead.
func TestRebuildDump(t *testing.T) {
	paths := rebuildProtos(t)
	files := append(readRebuildDescriptors(t, paths),
		protodesc.ToFileDescriptorProto(descriptorpb.File_google_protobuf_descriptor_proto))

	var want []byte
	if !*update {
		var err error
		if want, err = os.ReadFile(rebuildGolden); err != nil {
			t.Fatalf("%v (run go test -update to create it)", err)
		}
	}

	for order := range permutations(files) {
		names := make([]string, len(order))
		for i, pb := range order {
			names[i] = pb.GetName()
		}
		scanner := New(nil)
		for _, pb := range order {
			if err := scanner.register(pb); err != nil {
				t.Fatalf("order %v: register %s: %v", names, pb.GetName(), err)
			}
		}

		var buf bytes.Buffer
		if err := scanner.Dump(&buf); err != nil {
			t.Fatalf("order %v: Dump: %v", names, err)
		}

		if want == nil {
			// With -update, the first order gives the golden output and
			// every other order must still match it.
			want = slices.Clone(buf.Bytes())
			if err := os.WriteFile(rebuildGolden, want, 0o644); err != nil {
				t.Fatal(err)
			}
			t.Logf("updated %s", rebuildGolden)
			continue
		}
		if diff := cmp.Diff(string(want), buf.String()); diff != "" {
			t.Fatalf("order %v: Dump output differs from %s (-want +got):\n%s", names, rebuildGolden, diff)
		}
	}
}

// permutations yields every ordering of s. The slice it yields is reused, so
// it must not be kept past each iteration.
func permutations[T any](s []T) func(yield func([]T) bool) {
	return func(yield func([]T) bool) {
		p := slices.Clone(s)
		var permute func(k int) bool
		permute = func(k int) bool {
			if k == len(p) {
				return yield(p)
			}
			for i := k; i < len(p); i++ {
				p[k], p[i] = p[i], p[k]
				if !permute(k + 1) {
					return false
				}
				p[k], p[i] = p[i], p[k]
			}
			return true
		}
		permute(0)
	}
}
