package scanner

import (
	"fmt"
	"strconv"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
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

// fileProto returns fd as a FileDescriptorProto, with the options set by
// SetFileOption applied.
func (s *Scanner) fileProto(fd protoreflect.FileDescriptor) *descriptorpb.FileDescriptorProto {
	pb := protodesc.ToFileDescriptorProto(fd) // its options are a copy
	if s.fileOptions != nil {
		if pb.Options == nil {
			pb.Options = new(descriptorpb.FileOptions)
		}
		proto.Merge(pb.Options, s.fileOptions)
	}
	return pb
}
