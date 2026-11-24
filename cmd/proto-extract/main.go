package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"strings"

	"github.com/gbarr/protodump/scanner"
)

func main() {
	s := scanner.New(nil)

	output := flag.String("output", "", "directory to output to")
	flag.Func("option", "set file option `name=value` in every file written, such as\n"+
		"go_package=example.com/proto; may be repeated", func(opt string) error {
		name, value, ok := strings.Cut(opt, "=")
		if !ok {
			return fmt.Errorf("%q is not name=value", opt)
		}
		return s.SetFileOption(name, value)
	})
	flag.Func("go_package", "write each proto package as a Go package under import `prefix`,\n"+
		"in a directory of its own, overriding any go_package set by -option", func(prefix string) error {
		s.SetGoPackage(prefix)
		return nil
	})
	flag.Parse()
	args := flag.Args()
	if *output == "" || len(args) != 1 {
		log.Fatal("proto-extract -output DEST [-go_package prefix] [-option name=value]... SOURCE")
	}

	if err := s.Scan(args[0]); err != nil {
		// Descriptors that could not be added, and files or directories
		// under SOURCE that could not be read, are reported, but the files
		// that were found are still dumped.
		if _, ok := errors.AsType[scanner.Errors](err); !ok {
			log.Fatal(err)
		}
		log.Print(err)
	}

	if s.Files().NumFiles() == 0 {
		log.Fatal("no descriptors found")
	}

	written, err := s.WriteFiles(*output)
	if err != nil {
		log.Fatal(err)
	}
	if written == 0 {
		// Only files that are never written, such as descriptor.proto.
		log.Fatal("no descriptors to write")
	}

	fmt.Printf("wrote %d descriptors to %s\n", written, *output)
}
