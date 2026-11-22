package scanner

import (
	"errors"
	"fmt"
	"slices"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

// rebuild returns a new registry holding the files in files, registered so
// that each file comes after the files it imports. Files are registered with
// AllowUnresolvable, so one registered before a file it imports was found
// holds placeholders for that file's types; rebuilding in import order
// resolves them. Imports missing from files stay unresolved.
//
// Custom options are resolved too. When a descriptor is first decoded, the
// extensions that define its custom options are not known, so their values
// are kept as unknown fields. rebuild decodes each file's options again
// using the extensions in the file itself and in the files already rebuilt,
// which include everything it imports. Options whose extensions are still
// missing, or depend on types that are, stay unknown.
//
// Files that cannot be rebuilt are skipped; if any are, rebuild returns an
// error for each in an Errors, along with the registry of the rest. A file
// whose options cannot be decoded again is still registered, with them left
// unknown, and the error is reported in the same way. An import cycle, which
// protoc does not allow, is broken at the import that closes it.
func rebuild(files *protoregistry.Files) (*protoregistry.Files, Errors) {
	var paths []string
	files.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		paths = append(paths, fd.Path())
		return true
	})
	// Sort so that the order, and so any errors, do not depend on the
	// registry's map order.
	slices.Sort(paths)

	rebuilt := new(protoregistry.Files)
	rebuiltTypes := dynamicpb.NewTypes(rebuilt)
	visited := make(map[string]bool, len(paths))
	var errs Errors

	var visit func(path string)
	visit = func(path string) {
		if visited[path] {
			return
		}
		// Mark before visiting the imports, so that a cycle ends here.
		visited[path] = true

		fd, err := files.FindFileByPath(path)
		if err != nil {
			return // an import that is not in files
		}
		imports := fd.Imports()
		for i := range imports.Len() {
			visit(imports.Get(i).Path())
		}

		fileOptions := protodesc.FileOptions{AllowUnresolvable: true}
		pb := protodesc.ToFileDescriptorProto(fd)
		descriptor, err := fileOptions.New(pb, newImportResolver(rebuilt, pb))
		if err != nil {
			errs = append(errs, fmt.Errorf("rebuild %s: create FileDescriptor: %w", path, err))
			return
		}

		// descriptor's own extensions are needed as well as those already
		// rebuilt, since a file can use the options it defines.
		resolved, err := resolveOptions(pb, rebuiltTypes, descriptor)
		if err == nil {
			descriptor, err = fileOptions.New(resolved, newImportResolver(rebuilt, resolved))
		}
		if err != nil {
			// Keep the descriptor built from pb, with its options unknown.
			errs = append(errs, fmt.Errorf("rebuild %s: resolve options: %w", path, err))
		}

		if err := rebuilt.RegisterFile(descriptor); err != nil {
			errs = append(errs, fmt.Errorf("rebuild %s: register: %w", path, err))
		}
	}

	for _, path := range paths {
		visit(path)
	}

	return rebuilt, errs
}

// resolveOptions returns a copy of pb decoded again with its extensions
// resolved by types and by the extensions declared in fd, the descriptor
// built from pb. Custom options that were unknown fields become extension
// fields wherever their extension can be found.
func resolveOptions(pb *descriptorpb.FileDescriptorProto, types *dynamicpb.Types,
	fd protoreflect.FileDescriptor,
) (*descriptorpb.FileDescriptorProto, error) {
	// fd is not registered in the rebuilt registry yet, as it is about to be
	// replaced, so its extensions come from a registry of its own.
	local := new(protoregistry.Files)
	if err := local.RegisterFile(fd); err != nil {
		return nil, err
	}

	data, err := proto.Marshal(pb)
	if err != nil {
		return nil, err
	}
	resolved := new(descriptorpb.FileDescriptorProto)
	unmarshal := proto.UnmarshalOptions{
		Resolver: extensionResolver{types, dynamicpb.NewTypes(local)},
	}
	if err := unmarshal.Unmarshal(data, resolved); err != nil {
		return nil, err
	}
	return resolved, nil
}

// extensionResolver finds extension types in each of its registries in
// turn. It skips extensions that extend or hold a placeholder type, as one
// whose definition was never found cannot be decoded reliably, so their
// values stay unknown fields.
type extensionResolver []*dynamicpb.Types

func (r extensionResolver) FindExtensionByName(field protoreflect.FullName) (protoreflect.ExtensionType, error) {
	for _, types := range r {
		xt, err := types.FindExtensionByName(field)
		if err == nil && resolvable(xt) {
			return xt, nil
		}
		if err != nil && !errors.Is(err, protoregistry.NotFound) {
			return nil, err
		}
	}
	return nil, protoregistry.NotFound
}

func (r extensionResolver) FindExtensionByNumber(message protoreflect.FullName,
	field protoreflect.FieldNumber,
) (protoreflect.ExtensionType, error) {
	for _, types := range r {
		xt, err := types.FindExtensionByNumber(message, field)
		if err == nil && resolvable(xt) {
			return xt, nil
		}
		if err != nil && !errors.Is(err, protoregistry.NotFound) {
			return nil, err
		}
	}
	return nil, protoregistry.NotFound
}

// resolvable reports whether xt's extendee and value type are both fully
// resolved, rather than placeholders.
func resolvable(xt protoreflect.ExtensionType) bool {
	xd := xt.TypeDescriptor()
	if xd.ContainingMessage().IsPlaceholder() {
		return false
	}
	if md := xd.Message(); md != nil && md.IsPlaceholder() {
		return false
	}
	if ed := xd.Enum(); ed != nil && ed.IsPlaceholder() {
		return false
	}
	return true
}
