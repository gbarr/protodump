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
		index := start + n - len(protoExtn)

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

		descriptor, err := extractDescriptor(data[start:])
		if err != nil {
			offset = index + 1
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
// filename field (tag magicByte) whose value ends in protoExtn.
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
			offset = index + 1
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

		// check we have found a valid field
		end := index + len(protoExtn)
		if _, _, n := protowire.ConsumeField(data[start:end]); n != end-start {
			offset = index + 1 // skip past found protoExtn
			continue
		}
		return start
	}
	return -1
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

func extractDescriptor(data []byte) ([]byte, error) {
	position := 0
	for {
		_, _, length := protowire.ConsumeField(data[position:])

		// descriptors are terminated by a null tag, which is an error from ConsumeField
		if length < 0 {
			v, n := protowire.ConsumeVarint(data[position:])
			if n < 0 {
				return nil, protowire.ParseError(n)
			}

			// found the null tag
			if v == 0 {
				return data[:position], nil
			}

			return nil, protowire.ParseError(length)
		}

		position += length

		if position == len(data) {
			return data, nil
		}

		// Don't read beyond data bounds
		if position > len(data) {
			return nil, io.ErrUnexpectedEOF
		}
	}
}
