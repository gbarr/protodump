package scanner

import (
	"fmt"
	"path"
	"strconv"
	"strings"

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

// SetGoPackage lays out the files that Dump and WriteFiles write as Go
// packages under the import path prefix, one directory per proto package.
// A file in proto package Foo.Bar is written to the directory foo_barpb,
// named by the proto package in lower case with each "." replaced by "_"
// and "pb" added, and its go_package option is set to prefix/foo_barpb. The
// file keeps its base name, so Foo/Bar/FooBar.proto becomes
// foo_barpb/FooBar.proto, and imports of it are changed to match. A file
// with no package is written to the top directory, with go_package set to
// prefix.
//
// This go_package replaces any set by SetFileOption. Files in package
// google.protobuf, the well-known types, are left where they are, with
// their own go_package, as they are provided by protoc and the Go protobuf
// module. If two files would be written to the same path, the first by
// original path is kept and the other is reported as an error.
//
// An empty prefix turns the layout off.
func (s *Scanner) SetGoPackage(prefix string) {
	s.goPackage = strings.TrimSuffix(prefix, "/")
}

// wellKnownPackage is the proto package of the well-known types, which
// SetGoPackage does not move.
const wellKnownPackage = "google.protobuf"

// goPackageDir returns the directory, and the Go package name, for proto
// package pkg: lower case, with each "." replaced by "_", and "pb" added.
// It returns "" for no package.
func goPackageDir(pkg protoreflect.FullName) string {
	if pkg == "" {
		return ""
	}
	return strings.ToLower(strings.ReplaceAll(string(pkg), ".", "_")) + "pb"
}

// layout returns the new path of each file in s's registry under the
// SetGoPackage layout, or nil if it is off. A file that would take a path
// already taken is given the path "", so that rebuild leaves it out, and
// reported in the returned Errors.
func (s *Scanner) layout() (map[string]string, Errors) {
	if s.goPackage == "" {
		return nil, nil
	}
	paths := make(map[string]string)
	taken := make(map[string]string) // new path to original path
	var errs Errors
	for _, original := range sortedPaths(s.files) {
		fd, err := s.files.FindFileByPath(original)
		if err != nil || fd.Package() == wellKnownPackage {
			continue
		}
		moved := path.Join(goPackageDir(fd.Package()), path.Base(original))
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

// rebuild rebuilds s's registry, under the SetGoPackage layout if it is on.
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
	if s.goPackage != "" && fd.Package() != wellKnownPackage {
		if pb.Options == nil {
			pb.Options = new(descriptorpb.FileOptions)
		}
		pb.Options.GoPackage = new(path.Join(s.goPackage, goPackageDir(fd.Package())))
	}
	return pb
}
