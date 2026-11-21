// Package protodump writes .proto source files from protobuf file
// descriptors.
package protodump

import (
	"bufio"
	"fmt"
	"io"
	"iter"
	"math"
	"slices"
	"strconv"
	"strings"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
)

type dumper struct {
	w      *bufio.Writer
	fd     protoreflect.FileDescriptor
	pb     *descriptorpb.FileDescriptorProto
	indent string

	// Blank-line state for the current scope: whether nothing has been
	// written in it yet, and whether the last thing written was a block or
	// the end of a section, which is followed by a blank line. It is updated
	// by beginItem, endSection and writeBlock:
	//
	//	scopeStart  afterBlock  state                          blank line before next item
	//	true        false       start of the file or a block   never
	//	true        true        does not occur                 -
	//	false       false       after a single line            only if the item is a block
	//	false       true        after a block or section end   always
	scopeStart bool
	afterBlock bool
}

func (d *dumper) increaseIndent() { d.indent += "  " }
func (d *dumper) decreaseIndent() { d.indent = d.indent[2:] }

// The write helpers ignore errors: d.w is a bufio.Writer, which keeps the
// first error and returns it from Flush at the end of Dump.

func (d *dumper) writeString(s string)              { _, _ = d.w.WriteString(s) }
func (d *dumper) writeByte(c byte)                  { _ = d.w.WriteByte(c) }
func (d *dumper) writef(format string, args ...any) { _, _ = fmt.Fprintf(d.w, format, args...) }

// writeLinef writes one line at the current indent, formatted from format
// and args. The trailing newline is added; format should not include it.
func (d *dumper) writeLinef(format string, args ...any) {
	d.beginItem(false)
	d.writeRawLinef(format, args...)
}

// writeRawLinef writes one line at the current indent without blank-line
// handling.
func (d *dumper) writeRawLinef(format string, args ...any) {
	d.writeString(d.indent)
	d.writef(format, args...)
	d.writeByte('\n')
}

// beginItem writes a blank line, if needed, before an item in the current
// scope. Blocks are separated from whatever precedes and follows them; runs
// of single lines are not separated, and nothing is written at the start of
// a scope.
func (d *dumper) beginItem(block bool) {
	if !d.scopeStart && (block || d.afterBlock) {
		d.writeByte('\n')
	}
	d.scopeStart = false
	d.afterBlock = block
}

// endSection marks the end of a run of lines, such as the imports, so the
// next item is separated from it by a blank line.
func (d *dumper) endSection() {
	if !d.scopeStart {
		d.afterBlock = true
	}
}

// writeBlock writes an indented header line formatted from format and args,
// followed by " {", then body one indent level deeper, then a closing "}".
func (d *dumper) writeBlock(body func(), format string, args ...any) {
	d.beginItem(true)
	d.writeRawLinef(format+" {", args...)

	d.increaseIndent()
	d.scopeStart, d.afterBlock = true, false
	body()
	d.decreaseIndent()

	d.scopeStart, d.afterBlock = false, true
	d.writeRawLinef("}")
}

// Dump writes .proto source for pb to w. It returns the first error from
// writing to w.
//
// Imports are resolved with resolver, which may be nil. References to types
// that cannot be resolved still work, but are written fully qualified because
// their package is unknown.
func Dump(w io.Writer, pb *descriptorpb.FileDescriptorProto, resolver protodesc.Resolver) error {
	if resolver == nil {
		resolver = new(protoregistry.Files)
	}
	fd, err := protodesc.FileOptions{AllowUnresolvable: true}.New(pb, resolver)
	if err != nil {
		return fmt.Errorf("building descriptor for %s: %w", pb.GetName(), err)
	}

	// bufio.Writer records the first write error and returns it from Flush,
	// so the individual writes need not check for errors.
	d := dumper{w: bufio.NewWriter(w), fd: fd, pb: pb, scopeStart: true}
	if err := d.writeHeader(); err != nil {
		return err
	}
	d.writePackage()
	d.writeImports()
	d.writeFileOptions()
	d.writeServices()
	d.writeEnums(fd.Enums())
	d.writeDeclarations(fd.Messages(), nil, fd.Extensions())

	return d.w.Flush()
}

func (d *dumper) writeHeader() error {
	if d.fd.Syntax() == protoreflect.Editions {
		edition, err := editionString(d.pb.GetEdition())
		if err != nil {
			return err
		}
		d.writeLinef("edition = \"%s\";", edition)
		d.endSection()
		return nil
	}
	d.writeLinef("syntax = \"%s\";", d.fd.Syntax())
	d.endSection()

	return nil
}

func (d *dumper) writeFileOptions() {
	options := optionAssignments(d.fd.Options())
	for _, option := range options {
		d.writeLinef("option %s;", option)
	}
	d.endSection()
}

func (d *dumper) writeImports() {
	fileImports := d.fd.Imports()

	for _, fileImport := range forEach(fileImports) {
		public := ""
		if fileImport.IsPublic {
			public = "public "
		}
		d.writeLinef("import %s\"%s\";", public, escapeString(fileImport.Path()))
	}
	d.endSection()
}

func (d *dumper) writePackage() {
	packageName := d.fd.FullName()
	if packageName != "" {
		d.writeLinef("package %s;", packageName)
		d.endSection()
	}
}

func (d *dumper) writeServices() {
	fileServices := d.fd.Services()

	for _, service := range forEach(fileServices) {
		d.writeBlock(func() {
			d.writeOptionLines(service.Options())
			for _, method := range forEach(service.Methods()) {
				d.writeMethod(method)
			}
		}, "service %s", service.Name())
	}
}

func (d *dumper) writeMethod(method protoreflect.MethodDescriptor) {
	streamClient := ""
	if method.IsStreamingClient() {
		streamClient = "stream "
	}
	streamServer := ""
	if method.IsStreamingServer() {
		streamServer = "stream "
	}

	header := fmt.Sprintf("rpc %s (%s%s) returns (%s%s)",
		method.Name(),
		streamClient, typeRef(method, method.Input()),
		streamServer, typeRef(method, method.Output()),
	)

	// Without options, end with ";" rather than "{}": protoc records an empty
	// body as an empty options message, which the original does not have.
	options := optionAssignments(method.Options())
	if len(options) == 0 {
		d.writeLinef("%s;", header)
		return
	}
	d.writeBlock(func() {
		for _, option := range options {
			d.writeLinef("option %s;", option)
		}
	}, "%s", header)
}

func (d *dumper) writeMessage(message protoreflect.MessageDescriptor) {
	d.writeBlock(func() {
		d.writeMessageBody(message)
	}, "message %s", message.Name())
}

// writeMessageBody writes the declarations inside a message's braces. It is
// shared by message definitions and proto2 group fields.
func (d *dumper) writeMessageBody(message protoreflect.MessageDescriptor) {
	d.writeOptionLines(message.Options())
	d.writeReservedNames(message.ReservedNames())

	for _, reservedRange := range forEach(message.ReservedRanges()) {
		// lower is inclusive, upper is exclusive
		lower, upper := reservedRange[0], reservedRange[1]-1
		d.writeReservedRange(int32(lower), int32(upper), int32(protowire.MaxValidNumber))
	}

	d.writeExtensionRanges(message)
	d.writeEnums(message.Enums())
	d.writeDeclarations(message.Messages(), message.Fields(), message.Extensions())
}

// writeReservedNames writes a reserved statement for each name in names.
// Editions files write reserved names as bare identifiers; proto2 and proto3
// require them to be quoted strings.
func (d *dumper) writeReservedNames(names protoreflect.Names) {
	bare := d.fd.Syntax() == protoreflect.Editions
	for _, name := range forEach(names) {
		if bare {
			d.writeLinef("reserved %s;", name)
		} else {
			d.writeLinef("reserved \"%s\";", escapeString(string(name)))
		}
	}
}

// writeReservedRange writes a reserved range statement. lower and upper are
// both inclusive; upper == max is written as "max".
func (d *dumper) writeReservedRange(lower, upper, max int32) {
	switch {
	case upper == max:
		d.writeLinef("reserved %d to max;", lower)
	case upper > lower:
		d.writeLinef("reserved %d to %d;", lower, upper)
	default:
		d.writeLinef("reserved %d;", lower)
	}
}

func (d *dumper) writeEnums(enums protoreflect.EnumDescriptors) {
	for _, e := range forEach(enums) {
		d.writeEnum(e)
	}
}

func (d *dumper) writeEnum(enum protoreflect.EnumDescriptor) {
	d.writeBlock(func() {
		d.writeOptionLines(enum.Options())
		d.writeReservedNames(enum.ReservedNames())

		// Unlike message ranges, enum reserved ranges are inclusive at both ends.
		for _, reservedRange := range forEach(enum.ReservedRanges()) {
			lower, upper := reservedRange[0], reservedRange[1]
			d.writeReservedRange(int32(lower), int32(upper), math.MaxInt32)
		}

		for _, value := range forEach(enum.Values()) {
			d.writeLinef("%s = %d%s;", value.Name(), value.Number(), optionList(optionAssignments(value.Options())))
		}
	}, "enum %s", enum.Name())
}

func (d *dumper) writeExtension(extend protoreflect.ExtensionDescriptor) {
	d.writeBlock(func() {
		d.writeField(extend)
	}, "extend %s", typeRef(extend, extend.ContainingMessage()))
}

// writeDeclarations writes a scope's nested messages, fields and extensions
// so that recompiling the output preserves the order of all three lists. For
// a file, fields is nil.
//
// Fields are written in declaration order as units: a single field, or a
// real oneof as a block where its first field appears (protoc requires a
// oneof's fields to be declared together). Proto3 optional fields belong to a
// synthetic oneof and are written as plain fields. Each extension is written
// as its own extend block, in declaration order.
//
// A map field or proto2 group field, including a group in an extend block,
// adds its message to the scope's message list at the point the field is
// declared. So messages are written in order, and on reaching one owned by a
// field or extension, the field units or extensions up to and including its
// owner are written first.
func (d *dumper) writeDeclarations(messages protoreflect.MessageDescriptors, fields protoreflect.FieldDescriptors, extensions protoreflect.ExtensionDescriptors) {
	var units []protoreflect.FieldDescriptor // first field of each unit
	oneofUnit := map[protoreflect.FullName]int{}
	fieldOwner := map[protoreflect.FullName]int{} // owned message -> unit

	if fields != nil {
		for _, field := range forEach(fields) {
			unit := len(units)
			oneof := field.ContainingOneof()
			if oneof != nil && !oneof.IsSynthetic() {
				if i, ok := oneofUnit[oneof.FullName()]; ok {
					unit = i
				} else {
					oneofUnit[oneof.FullName()] = unit
					units = append(units, field)
				}
			} else {
				units = append(units, field)
			}
			if field.IsMap() || isProto2Group(field) {
				fieldOwner[field.Message().FullName()] = unit
			}
		}
	}

	extOwner := map[protoreflect.FullName]int{} // owned message -> extension
	for i, extend := range forEach(extensions) {
		if isProto2Group(extend) {
			extOwner[extend.Message().FullName()] = i
		}
	}

	nextUnit := 0
	writeUnitsThrough := func(last int) {
		for ; nextUnit <= last; nextUnit++ {
			field := units[nextUnit]
			if oneof := field.ContainingOneof(); oneof != nil && !oneof.IsSynthetic() {
				d.writeOneOf(oneof)
			} else {
				d.writeField(field)
			}
		}
	}
	nextExt := 0
	writeExtensionsThrough := func(last int) {
		for ; nextExt <= last; nextExt++ {
			d.writeExtension(extensions.Get(nextExt))
		}
	}

	for _, nested := range forEach(messages) {
		if unit, ok := fieldOwner[nested.FullName()]; ok {
			writeUnitsThrough(unit)
			continue
		}
		if ext, ok := extOwner[nested.FullName()]; ok {
			writeExtensionsThrough(ext)
			continue
		}
		d.writeMessage(nested)
	}
	writeUnitsThrough(len(units) - 1)
	writeExtensionsThrough(extensions.Len() - 1)
}

func (d *dumper) writeField(field protoreflect.FieldDescriptor) {
	var header string
	group := isProto2Group(field)
	if group {
		// The group's message name is the declared name; the field name is
		// its lowercased form and is not written.
		header = fmt.Sprintf("%sgroup %s = %d", fieldCardinality(field), field.Message().Name(), field.Number())
	} else {
		header = fmt.Sprintf("%s%s %s = %d", fieldCardinality(field), fieldType(field), field.Name(), field.Number())
	}
	options := []string{}

	if field.HasDefault() {
		options = append(options, "default = "+fieldDefault(field))
	}

	// json_name is only written when it differs from the default, since protoc
	// records a json_name for every field. It is not allowed on extensions.
	if !field.IsExtension() && field.JSONName() != defaultJSONName(string(field.Name())) {
		options = append(options, fmt.Sprintf("json_name = \"%s\"", escapeString(field.JSONName())))
	}

	// Editions features such as message_encoding = DELIMITED (a group-kind
	// field outside proto2) and field_presence = LEGACY_REQUIRED are field
	// options, so they are written here too.
	options = append(options, optionAssignments(field.Options())...)
	header += optionList(options)

	if group {
		d.writeBlock(func() {
			d.writeMessageBody(field.Message())
		}, "%s", header)
		return
	}

	d.writeLinef("%s;", header)
}

func (d *dumper) writeOneOf(oneof protoreflect.OneofDescriptor) {
	d.writeBlock(func() {
		d.writeOptionLines(oneof.Options())
		for _, field := range forEach(oneof.Fields()) {
			d.writeField(field)
		}
	}, "oneof %s", oneof.Name())
}

func (d *dumper) writeExtensionRanges(message protoreflect.MessageDescriptor) {
	for i, ranges := range forEach(message.ExtensionRanges()) {
		options := optionList(optionAssignments(message.ExtensionRangeOptions(i)))
		d.writeLinef("extensions %d to %d%s;", ranges[0], ranges[1]-1, options)
	}
}

func editionString(edition descriptorpb.Edition) (string, error) {
	editionStr := edition.String()
	if rest, ok := strings.CutPrefix(editionStr, "EDITION_"); ok {
		if _, err := strconv.Atoi(rest); err == nil {
			return rest, nil
		}
	}
	return "", fmt.Errorf("not a source edition: %s", editionStr)
}

func escapeString(s string) string {
	var b strings.Builder
	for i := range len(s) {
		switch c := s[i]; {
		case c == '\\':
			b.WriteString(`\\`)
		case c == '"':
			b.WriteString(`\"`)
		case c == '\n':
			b.WriteString(`\n`)
		case c == '\r':
			b.WriteString(`\r`)
		case c == '\t':
			b.WriteString(`\t`)
		case c < 0x20 || c >= 0x7f:
			fmt.Fprintf(&b, `\%03o`, c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// indexedList is a list with indexed access, as implemented by the
// protoreflect descriptor lists, Names, FieldRanges, EnumRanges and List.
type indexedList[T any] interface {
	// Len reports the number of elements.
	Len() int
	// Get returns the ith element. It panics if out of bounds.
	Get(i int) T
}

// forEach returns an iterator over the index and element of each item in
// list, so protoreflect lists can be used with range instead of a Len/Get
// loop:
//
//	for _, field := range forEach(message.Fields()) { ... }
//
// It works with any type that has Len and Get methods, such as the descriptor
// lists (FieldDescriptors, EnumValueDescriptors, ...), Names, FieldRanges and
// protoreflect.List.
func forEach[L indexedList[T], T any](list L) iter.Seq2[int, T] {
	return func(yield func(int, T) bool) {
		for i := range list.Len() {
			if !yield(i, list.Get(i)) {
				return
			}
		}
	}
}

// fieldCardinality returns the label prefix for field, including a trailing
// space, or "" if the field takes no label.
func fieldCardinality(field protoreflect.FieldDescriptor) string {
	// map<K, V> fields are implicitly repeated and take no label.
	if field.IsMap() {
		return ""
	}

	switch field.Cardinality() {
	case protoreflect.Optional:
		// HasOptionalKeyword is true for proto2 optional fields outside a oneof
		// and for proto3 explicit "optional" fields. It is false for implicit
		// proto3 fields, editions fields and members of a real oneof, none of
		// which may carry the label.
		if field.HasOptionalKeyword() {
			return "optional "
		}
	case protoreflect.Repeated:
		return "repeated "
	case protoreflect.Required:
		if fileSyntax(field) == protoreflect.Proto2 {
			return "required "
		}
	}

	return ""
}

// fieldDefault returns field's default value as a .proto literal.
func fieldDefault(field protoreflect.FieldDescriptor) string {
	switch field.Kind() {
	case protoreflect.StringKind:
		return `"` + escapeString(field.Default().String()) + `"`
	case protoreflect.BytesKind:
		return `"` + escapeString(string(field.Default().Bytes())) + `"`
	case protoreflect.EnumKind:
		return string(field.DefaultEnumValue().Name())
	case protoreflect.FloatKind, protoreflect.DoubleKind:
		return floatLiteral(field.Default().Float(), field.Kind())
	default:
		return field.Default().String()
	}
}

// floatLiteral formats f as a .proto float literal. Go formats infinities and
// NaN as +Inf, -Inf and NaN; .proto uses inf, -inf and nan.
func floatLiteral(f float64, kind protoreflect.Kind) string {
	switch {
	case math.IsInf(f, 1):
		return "inf"
	case math.IsInf(f, -1):
		return "-inf"
	case math.IsNaN(f):
		return "nan"
	}
	bits := 64
	if kind == protoreflect.FloatKind {
		bits = 32
	}
	return strconv.FormatFloat(f, 'g', -1, bits)
}

// writeOptionLines writes each option set in opts as an "option" statement.
func (d *dumper) writeOptionLines(opts proto.Message) {
	for _, option := range optionAssignments(opts) {
		d.writeLinef("option %s;", option)
	}
}

// optionList formats options as a bracketed list to follow a declaration,
// with a leading space, or "" if there are none.
func optionList(options []string) string {
	if len(options) == 0 {
		return ""
	}
	return " [" + strings.Join(options, ", ") + "]"
}

// optionAssignments returns the options set in opts as "name = value" strings
// in .proto syntax. Standard options
// come first in field order, then custom options in name order.
//
// Message-valued options are written per field with dotted names, such as
// "features.field_presence = EXPLICIT", and repeated options once per element.
func optionAssignments(opts proto.Message) []string {
	if opts == nil {
		return nil
	}
	m := opts.ProtoReflect()
	if !m.IsValid() {
		return nil
	}
	// FIXME(gbarr): unknown options are not handled. Custom options whose
	// extensions were not available when the descriptor was parsed are stored
	// as unknown fields (m.GetUnknown()) and are not written.
	return appendOptions(nil, "", m)
}

func appendOptions(out []string, prefix string, m protoreflect.Message) []string {
	for _, fd := range setFields(m) {
		if fd.IsExtension() {
			out = appendOption(out, prefix+"("+string(fd.FullName())+")", fd, m.Get(fd))
			continue
		}
		out = appendOption(out, prefix+string(fd.Name()), fd, m.Get(fd))
	}
	return out
}

// setFields returns the fields set in m: regular fields in declaration order,
// then extensions in name order.
func setFields(m protoreflect.Message) []protoreflect.FieldDescriptor {
	var fields, exts []protoreflect.FieldDescriptor
	for _, fd := range forEach(m.Descriptor().Fields()) {
		if m.Has(fd) {
			fields = append(fields, fd)
		}
	}
	m.Range(func(fd protoreflect.FieldDescriptor, _ protoreflect.Value) bool {
		if fd.IsExtension() {
			exts = append(exts, fd)
		}
		return true
	})
	slices.SortFunc(exts, func(a, b protoreflect.FieldDescriptor) int {
		return strings.Compare(string(a.FullName()), string(b.FullName()))
	})
	return append(fields, exts...)
}

func appendOption(out []string, name string, fd protoreflect.FieldDescriptor, v protoreflect.Value) []string {
	switch {
	case fd.IsList():
		for _, elem := range forEach(v.List()) {
			out = append(out, name+" = "+optionValue(fd, elem))
		}
	case fd.Message() != nil:
		sub := v.Message()
		before := len(out)
		out = appendOptions(out, name+".", sub)
		if len(out) == before {
			// A set but empty message must still be written to keep it set.
			out = append(out, name+" = {}")
		}
	default:
		out = append(out, name+" = "+optionValue(fd, v))
	}
	return out
}

// aggregateLiteral formats m in the text format aggregate syntax used for
// message-valued options, such as { name: "x" ids: 1 ids: 2 }. It is written
// here rather than with prototext, whose output deliberately varies.
func aggregateLiteral(m protoreflect.Message) string {
	var parts []string
	for _, fd := range setFields(m) {
		name := string(fd.Name())
		switch {
		case fd.IsExtension():
			name = "[" + string(fd.FullName()) + "]"
		case fd.Kind() == protoreflect.GroupKind:
			// Text format names a group field by its message name.
			name = string(fd.Message().Name())
		}
		v := m.Get(fd)
		if fd.IsList() {
			for _, elem := range forEach(v.List()) {
				parts = append(parts, name+": "+optionValue(fd, elem))
			}
			continue
		}
		parts = append(parts, name+": "+optionValue(fd, v))
	}
	if len(parts) == 0 {
		return "{}"
	}
	return "{ " + strings.Join(parts, " ") + " }"
}

// optionValue formats a single option value as a .proto literal.
func optionValue(fd protoreflect.FieldDescriptor, v protoreflect.Value) string {
	switch fd.Kind() {
	case protoreflect.BoolKind:
		return strconv.FormatBool(v.Bool())
	case protoreflect.EnumKind:
		if ev := fd.Enum().Values().ByNumber(v.Enum()); ev != nil {
			return string(ev.Name())
		}
		return strconv.Itoa(int(v.Enum()))
	case protoreflect.StringKind:
		return `"` + escapeString(v.String()) + `"`
	case protoreflect.BytesKind:
		return `"` + escapeString(string(v.Bytes())) + `"`
	case protoreflect.FloatKind, protoreflect.DoubleKind:
		return floatLiteral(v.Float(), fd.Kind())
	case protoreflect.MessageKind, protoreflect.GroupKind:
		// Elements of repeated message options use the aggregate syntax.
		return aggregateLiteral(v.Message())
	default:
		return v.String()
	}
}

// defaultJSONName returns the JSON name protoc derives for a field called
// name: underscores are removed and a lowercase letter following one is
// capitalised.
func defaultJSONName(name string) string {
	var b strings.Builder
	upper := false
	for i := range len(name) {
		c := name[i]
		if c == '_' {
			upper = true
			continue
		}
		if upper && 'a' <= c && c <= 'z' {
			c -= 'a' - 'A'
		}
		b.WriteByte(c)
		upper = false
	}
	return b.String()
}

// fileSyntax returns the syntax of the file declaring desc, or the zero
// Syntax if desc has no parent file, which matches no syntax-specific case.
func fileSyntax(desc protoreflect.Descriptor) protoreflect.Syntax {
	if file := desc.ParentFile(); file != nil {
		return file.Syntax()
	}
	return 0
}

// isProto2Group reports whether field is a proto2 group, which is written
// using the "group Name = N { ... }" syntax.
func isProto2Group(field protoreflect.FieldDescriptor) bool {
	return field.Kind() == protoreflect.GroupKind && fileSyntax(field) == protoreflect.Proto2
}

// typeRef returns a reference to target as written in from, a field or method.
//
// protoc resolves a relative name by searching outward from the scope of the
// reference, so a name declared in an enclosing message or service shadows
// the same name in the package. When target is in from's package and nothing
// in the scopes between from and the package declares the first component of
// the package-relative name, that shorter name is returned. Otherwise the name
// is fully qualified with a leading ".", which resolves from the root scope.
func typeRef(from, target protoreflect.Descriptor) string {
	fromFile, targetFile := from.ParentFile(), target.ParentFile()
	// ParentFile may be nil, e.g. for placeholder descriptors of unresolved
	// types; the package is then unknown, so qualify fully.
	if fromFile == nil || targetFile == nil || targetFile.Package() != fromFile.Package() {
		return "." + string(target.FullName())
	}
	pkg := fromFile.Package()

	rel := string(target.FullName())
	if pkg != "" {
		rel = strings.TrimPrefix(rel, string(pkg)+".")
	}
	first, _, _ := strings.Cut(rel, ".")

	for scope := from.Parent(); scope != nil; scope = scope.Parent() {
		if _, ok := scope.(protoreflect.FileDescriptor); ok {
			break
		}
		if declares(scope, protoreflect.Name(first)) {
			return "." + string(target.FullName())
		}
	}
	return rel
}

// declares reports whether scope declares a symbol called name. It is
// conservative: any symbol counts, even kinds that protoc skips when
// resolving a type name, so a false positive only costs a longer name.
func declares(scope protoreflect.Descriptor, name protoreflect.Name) bool {
	switch scope := scope.(type) {
	case protoreflect.MessageDescriptor:
		if scope.Fields().ByName(name) != nil ||
			scope.Oneofs().ByName(name) != nil ||
			scope.Messages().ByName(name) != nil ||
			scope.Enums().ByName(name) != nil ||
			scope.Extensions().ByName(name) != nil {
			return true
		}
		// Enum values are scoped as siblings of their enum, not inside it.
		for _, enum := range forEach(scope.Enums()) {
			if enum.Values().ByName(name) != nil {
				return true
			}
		}
	case protoreflect.ServiceDescriptor:
		return scope.Methods().ByName(name) != nil
	}
	return false
}

func fieldType(field protoreflect.FieldDescriptor) string {
	// Map fields have MessageKind (the synthesised entry message), so check
	// IsMap before switching on the kind.
	if field.IsMap() {
		return fmt.Sprintf("map<%s, %s>", fieldType(field.MapKey()), fieldType(field.MapValue()))
	}

	kind := field.Kind().String()

	switch kind {
	case "message", "group":
		return typeRef(field, field.Message())
	case "enum":
		return typeRef(field, field.Enum())
	default:
		return kind
	}
}
