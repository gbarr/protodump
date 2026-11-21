// Package scanner finds protobuf file descriptors embedded in files, such as
// compiled binaries, and registers them in a protoregistry.Files.
package scanner

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/gbarr/protodump"
)

// Errors is a list of errors reported together, such as the descriptors in a
// file that could not be added. errors.Is and errors.As look through each
// error in the list.
type Errors []error

// Error returns the errors' messages, one per line.
func (e Errors) Error() string {
	msgs := make([]string, len(e))
	for i, err := range e {
		msgs[i] = err.Error()
	}
	return strings.Join(msgs, "\n")
}

// Unwrap returns the errors in the list.
func (e Errors) Unwrap() []error {
	return e
}

// Scanner registers the file descriptors it finds in a protoregistry.Files.
type Scanner struct {
	files *protoregistry.Files
}

// New returns a Scanner that registers into files, or into a new, empty
// protoregistry.Files if files is nil.
func New(files *protoregistry.Files) *Scanner {
	if files == nil {
		files = new(protoregistry.Files)
	}
	return &Scanner{files: files}
}

// Files returns the registry the scanner registers into.
func (s *Scanner) Files() *protoregistry.Files {
	return s.files
}

// Scan scans source, which may be a file or a directory. A directory is
// scanned with ScanDir.
func (s *Scanner) Scan(source string) error {
	fi, err := os.Stat(source)
	if err != nil {
		return err
	}

	if fi.Mode().IsRegular() {
		return s.ScanFile(source)
	}

	return s.ScanDir(source)
}

// ScanDir scans every executable regular file under dir with ScanFile. It
// continues past files that fail and returns their errors joined.
func (s *Scanner) ScanDir(dir string) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }() // a directory handle; nothing to flush

	var scanErrors error
	err = fs.WalkDir(root.FS(), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		fileInfo, err := d.Info()
		if err != nil {
			return err
		}

		// only interested in files which are executable
		const ModeExec = fs.FileMode(0o111)
		if fileInfo.Mode()&ModeExec == 0 {
			return nil
		}
		if err := s.ScanFile(filepath.Join(dir, path)); err != nil {
			scanErrors = errors.Join(scanErrors, err)
		}
		return nil
	})
	return errors.Join(scanErrors, err)
}

// ScanFile scans the file named source for embedded file descriptors and
// file descriptor sets, and registers the files it finds. Descriptors that
// cannot be decoded or registered are skipped, and reported together as an
// Errors once the whole file has been scanned.
func (s *Scanner) ScanFile(source string) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	var errs Errors

	offset := 0
	for offset < len(data) {
		start := findDescriptor(data[offset:])
		if start == -1 {
			break
		}
		start += offset

		// n is the length of the filename field, which ends with protoExtn.
		_, _, n := protowire.ConsumeField(data[start:])

		isFileSet := false
		if start-offset > 2 {
			st2 := bytes.LastIndexByte(data[offset:start-1], magicByte)
			if st2 >= 20000 {
				_, _, setN := protowire.ConsumeField(data[offset+st2:])
				if setN > n {
					start = offset + st2
					isFileSet = true
				}
			}
		}

		descriptor := extractDescriptor(data[start:])
		if len(descriptor) == 0 {
			offset = start + n
			continue
		}

		if isFileSet {
			if err := s.addFileDescriptorSet(descriptor); err != nil {
				errs = append(errs, fmt.Errorf("addFileDescriptorSet %s:%d: %w", source, start, err))
			}
		} else {
			if err := s.addFileDescriptor(descriptor); err != nil {
				errs = append(errs, fmt.Errorf("addFileDescriptor %s:%d: %w", source, start, err))
			}
		}

		offset = start + len(descriptor)
	}

	// Return nil, not an empty Errors, so that a nil check still works.
	if len(errs) > 0 {
		return errs
	}
	return nil
}

const (
	protoExtn = ".proto"
	magicByte = 0xa // tag for BytesType(2) Number(1)

	// stepbackLength is the difference between index and start when the
	// filename length is the same as magicByte: the length byte, which is
	// then found instead of the tag, plus the name before the extension.
	stepbackLength = 1 + magicByte - len(protoExtn)
)

// findDescriptor returns the offset in data of the first candidate
// descriptor, or -1 if there is none. A candidate starts with a valid
// filename field (tag magicByte) whose value passes validName.
func findDescriptor(data []byte) int {
	// to locate the start of a protobuf descriptor,
	// first scan for the filename extension .proto
	// once found look for the protobuf filename tag byte preceding the name
	offset := 0
	for offset < len(data) {
		index := bytes.Index(data[offset:], []byte(protoExtn))
		if index == -1 {
			return -1
		}
		index += offset

		start := bytes.LastIndexByte(data[offset:index], magicByte)
		if start == -1 {
			offset = index + 1 // skip past found protoExtn
			continue
		}
		start += offset

		// If the filename length is the same as the tag value, then what we found
		// is actually the length and the preceding byte should also be the tag.
		// A name of length magicByte-1 preceded by a magicByte is just as valid
		// read from that byte, with its length as the first character of the
		// name. Names are unlikely to start with that control character, so
		// in that case keep the shorter name.
		if index-start == stepbackLength && start > 0 && data[start-1] == magicByte &&
			data[start+1] != magicByte-1 {
			start--
		}

		// check we have found a valid field, with the tag at start
		end := index + len(protoExtn)
		if name, n := protowire.ConsumeBytes(data[start+1 : end]); n != end-start-1 || !validName(name) {
			offset = index + 1 // skip past found protoExtn
			continue
		}
		return start
	}
	return -1
}

// validName reports whether name looks like a .proto file path as given to
// protoc: a relative, slash-separated path of ASCII letters, digits, '_',
// '-' and '.', with no empty elements, ending in protoExtn after a
// non-empty base name. It rejects names made from unrelated bytes that
// happen to precede a protoExtn in a binary.
func validName(name []byte) bool {
	base, ok := bytes.CutSuffix(name, []byte(protoExtn))
	if !ok || len(base) == 0 || base[0] == '/' || base[len(base)-1] == '/' ||
		bytes.Contains(base, []byte("//")) {
		return false
	}
	for _, c := range base {
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		case c == '_' || c == '-' || c == '.' || c == '/':
		default:
			return false
		}
	}
	return true
}

func (s *Scanner) addFileDescriptorSet(encoded []byte) error {
	set := new(descriptorpb.FileDescriptorSet)
	if err := proto.Unmarshal(encoded, set); err != nil {
		return fmt.Errorf("cannot unmarshal FileDescriptorSet: %w", err)
	}

	var errs []error
	for _, file := range set.File {
		if err := s.register(file); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}

func (s *Scanner) addFileDescriptor(encoded []byte) error {
	var pb descriptorpb.FileDescriptorProto
	err := proto.Unmarshal(encoded, &pb)
	if err != nil {
		return fmt.Errorf("cannot unmarshal FileDescriptor: %w", err)
	}
	return s.register(&pb)
}

func (s *Scanner) register(proto *descriptorpb.FileDescriptorProto) error {
	name := proto.GetName()
	if name == "" {
		return errors.New("missing Name")
	}

	if name == "google/protobuf/descriptor.proto" {
		return nil // silently skip. It will fail to register
	}

	// FIXME(gbarr): The same file is often found more than once
	// when scanning an application directory. For now we silently
	// assume they have identical contents
	if _, err := s.files.FindFileByPath(name); err == nil {
		return nil
	}

	fileOptions := protodesc.FileOptions{AllowUnresolvable: true}
	descriptor, err := fileOptions.New(proto, s.files)
	if err != nil {
		return fmt.Errorf("create FileDescriptor: %w", err)
	}
	if err := s.files.RegisterFile(descriptor); err != nil {
		return fmt.Errorf("register: %w", err)
	}

	return nil
}

// Dump writes .proto source for every registered file to w, in order of
// file path, each preceded by a comment line giving its path. Files that
// cannot be dumped are skipped; if any are, it returns an Errors with an
// error for each. An error writing to w is returned immediately.
func (s *Scanner) Dump(w io.Writer) error {
	var files []protoreflect.FileDescriptor
	s.files.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		files = append(files, fd)
		return true
	})
	slices.SortFunc(files, func(a, b protoreflect.FileDescriptor) int {
		return strings.Compare(a.Path(), b.Path())
	})

	var errs Errors
	var buf bytes.Buffer
	written := 0
	for _, fd := range files {
		// Dump into buf first so a file that fails leaves no partial output.
		buf.Reset()
		if err := protodump.Dump(&buf, protodesc.ToFileDescriptorProto(fd), s.files); err != nil {
			errs = append(errs, fmt.Errorf("dump failed %s: %w", fd.Path(), err))
			continue
		}

		sep := ""
		if written > 0 {
			sep = "\n"
		}
		if _, err := fmt.Fprintf(w, "%s// %s\n", sep, fd.Path()); err != nil {
			return err
		}
		if _, err := w.Write(buf.Bytes()); err != nil {
			return err
		}
		written++
	}

	if len(errs) > 0 {
		return errs
	}
	return nil
}

// WriteFiles writes .proto source for every registered file to dir, which is
// created if needed. File names that would leave dir are rejected. Files
// that cannot be written are skipped; if any are, it returns an Errors with
// an error for each.
func (s *Scanner) WriteFiles(dir string) error {
	err := os.MkdirAll(dir, 0o700)
	if err != nil {
		return err
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("cannot get absolute dir for %s: %w", dir, err)
	}
	// File names come from descriptors found in the scanned files, so write
	// through an os.Root to keep them inside the output directory.
	root, err := os.OpenRoot(absDir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }() // a directory handle; nothing to flush

	var errs Errors
	written := 0
	s.files.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		pd := protodesc.ToFileDescriptorProto(fd)
		var buf bytes.Buffer
		name := pd.GetName()
		if err := protodump.Dump(&buf, pd, s.files); err != nil {
			errs = append(errs, fmt.Errorf("dump failed %s: %w", name, err))
			return true
		}
		if err := writeFile(root, name, buf.Bytes()); err != nil {
			errs = append(errs, fmt.Errorf("dump failed %s: %w", name, err))
			return true
		}
		written++

		return true
	})

	if len(errs) > 0 {
		return errs
	}
	if total := s.files.NumFiles(); total != written {
		return fmt.Errorf("failed to write all files (wrote %d of %d)", written, total)
	}
	return nil
}

// writeFile writes data to filename, a slash-separated path, within root,
// creating any parent directories. Names that would leave root, such as
// "../x.proto" or absolute paths, are rejected.
func writeFile(root *os.Root, filename string, data []byte) error {
	name := filepath.FromSlash(filename)
	if dir := filepath.Dir(name); dir != "." {
		if err := root.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("failed to create directory for %s: %w", filename, err)
		}
	}
	if err := root.WriteFile(name, data, 0o666); err != nil {
		return fmt.Errorf("failed to write file %s: %w", filename, err)
	}
	return nil
}

var fileFields = (*descriptorpb.FileDescriptorProto)(nil).ProtoReflect().Descriptor().Fields()

// validFileField reports whether a field with number num and wire type typ can
// be part of a FileDescriptorProto.
func validFileField(num protowire.Number, typ protowire.Type) bool {
	fd := fileFields.ByNumber(num)
	if fd == nil {
		return false
	}
	var want protowire.Type
	switch fd.Kind() {
	case protoreflect.StringKind, protoreflect.BytesKind, protoreflect.MessageKind:
		return typ == protowire.BytesType
	case protoreflect.GroupKind:
		return typ == protowire.StartGroupType
	case protoreflect.Fixed32Kind, protoreflect.Sfixed32Kind, protoreflect.FloatKind:
		want = protowire.Fixed32Type
	case protoreflect.Fixed64Kind, protoreflect.Sfixed64Kind, protoreflect.DoubleKind:
		want = protowire.Fixed64Type
	default: // bool, enum, int32/64, uint32/64, sint32/64
		want = protowire.VarintType
	}
	// A repeated scalar field may also be packed into one BytesType field.
	return typ == want || (fd.IsList() && typ == protowire.BytesType)
}

// extractDescriptor returns the longest prefix of data made of fields that
// can belong to a FileDescriptorProto, in field-number order. data must
// start with the name field, as found by findDescriptor. The result is
// nil if nothing valid follows the name.
func extractDescriptor(data []byte) []byte {
	position := 0
	var last protowire.Number
	for {
		// This depends on the fact that protoc, or the plugin used,
		// outputs fields in order of field number. A number can repeat
		// but not go backwards.
		num, typ, length := protowire.ConsumeField(data[position:])
		if length < 0 || num < last || !validFileField(num, typ) {
			break
		}
		if num == last && !fileFields.ByNumber(num).IsList() {
			break // a non-repeated field cannot appear twice
		}
		last = num
		position += length
	}
	// A descriptor must have more than the name field. This also drops a
	// genuine descriptor holding only a name, such as an empty proto2 file
	// with no package, which is rare and has nothing to dump.
	if last <= 1 {
		return nil
	}
	return data[:position]
}
