package scanner

import (
	"fmt"
	"path"
	"strconv"
	"strings"
	"unicode"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
)

var fileOptionFields = (*descriptorpb.FileOptions)(nil).ProtoReflect().Descriptor().Fields()

// SetFileOption sets the file option name to value in every file that Dump
// and WriteFiles write, replacing any value the file already has. name is
// the name of a standard option, a field of google.protobuf.FileOptions such
// as go_package. value is taken as it is for a string option, as true or
// false for a bool, and as the name of an enum value, such as SPEED or
// LITE_RUNTIME. Custom options, and options that are messages or repeated,
// cannot be set.
func (s *Scanner) SetFileOption(name, value string) error {
	field := fileOptionFields.ByName(protoreflect.Name(name))
	if field == nil {
		return fmt.Errorf("unknown file option %q", name)
	}
	if field.IsList() {
		return fmt.Errorf("file option %s is repeated and cannot be set", name)
	}

	var v protoreflect.Value
	switch field.Kind() {
	case protoreflect.StringKind:
		v = protoreflect.ValueOfString(value)
	case protoreflect.BoolKind:
		b, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("file option %s: invalid bool %q", name, value)
		}
		v = protoreflect.ValueOfBool(b)
	case protoreflect.EnumKind:
		ev := field.Enum().Values().ByName(protoreflect.Name(value))
		if ev == nil {
			return fmt.Errorf("file option %s: unknown value %q for %s", name, value, field.Enum().FullName())
		}
		v = protoreflect.ValueOfEnum(ev.Number())
	default:
		return fmt.Errorf("file option %s is a %s and cannot be set", name, field.Kind())
	}

	if s.fileOptions == nil {
		s.fileOptions = new(descriptorpb.FileOptions)
	}
	s.fileOptions.ProtoReflect().Set(field, v)
	return nil
}

// SetGoPackage gives each file that Dump and WriteFiles write a Go package
// of its own under the import path prefix, by setting its go_package option
// to prefix and a name made from the file's base name: without ".proto", in
// lower case, with each "." and any other character that cannot be in a Go
// identifier replaced by "_", and "pb" added. Foo/Bar/FooBar.proto gets
// go_package prefix/foobarpb. This replaces any go_package set by
// SetFileOption. The files keep their paths unless SetRenameFiles is on.
//
// Giving each file a Go package of its own, rather than each proto package,
// means that Go packages can only import each other if the files do, which
// protoc does not allow, so the Go packages never form an import cycle.
//
// An empty prefix turns this off.
func (s *Scanner) SetGoPackage(prefix string) {
	s.goPackage = strings.TrimSuffix(prefix, "/")
}

// SetRenameFiles sets whether, with SetGoPackage, Dump and WriteFiles write
// each file into the directory named by its Go package, so that the output
// directory stands for the import path prefix. The file keeps its base name,
// so Foo/Bar/FooBar.proto becomes foobarpb/FooBar.proto, and imports of it
// are changed to match. If two files would be written to the same path, the
// first by original path is kept and the other is reported as an error. It
// has no effect without SetGoPackage, and is off by default.
func (s *Scanner) SetRenameFiles(on bool) {
	s.renameFiles = on
}

// goPackageDir returns the directory, and the Go package name, for the
// .proto file at path: its base name without ".proto", in lower case, with
// each character that cannot be in a Go identifier, such as ".", replaced
// by "_", and "pb" added.
func goPackageDir(file string) string {
	name := strings.ToLower(strings.TrimSuffix(path.Base(file), ".proto"))
	return strings.Map(func(r rune) rune {
		if r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		return '_'
	}, name) + "pb"
}

// layout returns the new path of each file in s's registry when
// SetGoPackage and SetRenameFiles are both on, or nil otherwise. A file
// that would take a path already taken is given the path "", so that
// rebuild leaves it out, and reported in the returned Errors. The well-known
// files are never written, so they keep their paths.
func (s *Scanner) layout() (map[string]string, Errors) {
	if s.goPackage == "" || !s.renameFiles {
		return nil, nil
	}
	paths := make(map[string]string)
	taken := make(map[string]string) // new path to original path
	var errs Errors
	for _, original := range sortedPaths(s.files) {
		if wellKnown(original) {
			continue
		}
		moved := path.Join(goPackageDir(original), path.Base(original))
		if other, ok := taken[moved]; ok {
			errs = append(errs, fmt.Errorf("layout %s: %s is already used by %s", original, moved, other))
			paths[original] = ""
			continue
		}
		taken[moved] = original
		paths[original] = moved
	}
	return paths, errs
}

// rebuild rebuilds s's registry, with the files renamed if SetGoPackage and
// SetRenameFiles are on.
func (s *Scanner) rebuild() (*protoregistry.Files, Errors) {
	paths, errs := s.layout()
	registry, rebuildErrs := rebuild(s.files, paths)
	return registry, append(errs, rebuildErrs...)
}

// fileProto returns fd, a file in the rebuilt registry, as a
// FileDescriptorProto, with the options set by SetFileOption and
// SetGoPackage applied.
func (s *Scanner) fileProto(fd protoreflect.FileDescriptor) *descriptorpb.FileDescriptorProto {
	pb := protodesc.ToFileDescriptorProto(fd) // its options are a copy
	if s.fileOptions != nil {
		if pb.Options == nil {
			pb.Options = new(descriptorpb.FileOptions)
		}
		proto.Merge(pb.Options, s.fileOptions)
	}
	if s.goPackage != "" {
		if pb.Options == nil {
			pb.Options = new(descriptorpb.FileOptions)
		}
		// Renaming keeps the base name, so this is the same either way.
		pb.Options.GoPackage = new(path.Join(s.goPackage, goPackageDir(fd.Path())))
	}
	return pb
}
