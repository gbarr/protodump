# protodump

[![Go Reference](https://pkg.go.dev/badge/github.com/gbarr/protodump.svg)](https://pkg.go.dev/github.com/gbarr/protodump)
[![Test](https://github.com/gbarr/protodump/actions/workflows/ci.yml/badge.svg)](https://github.com/gbarr/protodump/actions/workflows/ci.yml)
[![Go Version](https://img.shields.io/github/go-mod/go-version/gbarr/protodump)](go.mod)

Package `protodump` writes `.proto` source files from protobuf file
descriptors. It turns a compiled `FileDescriptorProto`, such as one embedded
in a binary or produced by `protoc --descriptor_set_out`, back into source that
`protoc` compiles to the same descriptor.

## Install

```sh
go get github.com/gbarr/protodump
```

## Usage

`Dump` writes the source for one file. Pass a resolver so that references to
types in imported files can be resolved; it may be `nil`.

```go
pb := protodesc.ToFileDescriptorProto(timestamppb.File_google_protobuf_timestamp_proto)
if err := protodump.Dump(os.Stdout, pb, nil); err != nil {
	log.Fatal(err)
}
```

To recover the `.proto` files embedded in a compiled binary, use the
`scanner` package to find their descriptors and dump them:

```go
s := scanner.New(nil)
if err := s.Scan("path/to/binary"); err != nil {
	// Descriptors that could not be added are reported as scanner.Errors;
	// anything else, such as a missing file, stops the scan.
	if _, ok := errors.AsType[scanner.Errors](err); !ok {
		log.Fatal(err)
	}
	log.Print(err)
}

// Write every file found to stdout, each preceded by a comment giving its path.
if err := s.Dump(os.Stdout); err != nil {
	log.Fatal(err)
}
```

`s.WriteFiles(dir)` writes each file to its own path under `dir` instead, and
returns how many files it wrote.

## What is supported

- proto2, proto3 and editions (2023 and 2024)
- messages, enums, services, including streaming methods
- fields of every cardinality, oneofs, proto3 `optional`, maps and proto2
  groups
- extensions and extension ranges, declared at file level or inside messages
- reserved names and ranges
- default values, including `inf`, `nan` and escaped strings and bytes
- custom `json_name` values
- options on every kind of declaration, including editions features and
  custom options whose definitions were resolved when the descriptor was
  parsed
- declaration order: recompiling the output gives the fields, nested messages
  and extensions in their original order

Type references are written relative to the current package where that is
unambiguous, and fully qualified (`.pkg.Type`) where a nearer name would
shadow the intended type or the package is unknown.

The tests compile every file in `testdata` with `protoc` and check that `Dump`
recreates each one exactly.

## Limitations

- Comments and the original formatting are not recovered; the output uses a
  fixed layout.
- Custom options that were not resolved when the descriptor was parsed are
  stored as unknown fields and are not written.
- Options with source retention, such as extension range `declaration` and
  `verification`, are removed by `protoc` and so are not in compiled
  descriptors.
- Without a resolver, types from other files in the same package are written
  fully qualified.

## proto-extract

The `cmd/proto-extract` command finds file descriptors embedded in compiled
binaries and writes them out as `.proto` files, using the `scanner` package.

```sh
go install github.com/gbarr/protodump/cmd/proto-extract@latest
proto-extract -output DEST SOURCE
```

`SOURCE` is a file, or a directory whose executable files are scanned. For
example, a macOS application is a directory, so a whole application can be
scanned at once, including any frameworks it contains:

```sh
proto-extract -output protos /Applications/Example.app
```

`-option name=value` sets a standard file option in every file written,
replacing any value the file already has. It may be repeated:

```sh
proto-extract -output protos -option go_package=example.com/proto /Applications/Example.app
```

`-go_package prefix` instead lays the output out as one Go package per
proto package, with `DEST` standing for the import path `prefix`. A file in
`package Foo.Bar;` is written to `DEST/foo_barpb/` under its base name, with
`go_package` set to `prefix/foo_barpb`, and imports of it are changed to
match. Files with no package go in `DEST` itself, with `go_package` set to
`prefix`. This `go_package` replaces one given with `-option`. The
well-known types in `google.protobuf` are not moved:

```sh
proto-extract -output protos -go_package example.com/proto /Applications/Example.app
```

## Development

```sh
go test ./...
```

The `testdata` tests use `protoc` when it is installed, and otherwise the
compiled descriptors saved in `testdata/descriptors.txtpb`. After changing
the testdata, regenerate that file with `protoc` installed:

```sh
go test -run TestDumpTestdata -update .
```

## License

MIT, see [LICENSE](LICENSE).
