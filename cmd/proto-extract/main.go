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
	goPackage := flag.String("go_package", "", "set go_package in each file to a Go package of its own under import\n"+
		"`prefix`, named after the file, overriding any go_package set by -option")
	rename := flag.Bool("rename", false, "with -go_package, write each file into the directory named by its Go\n"+
		"package, so that DEST stands for the import path prefix")
	debug := flag.Bool("debug", false, "also write each file's descriptor, exactly as found in SOURCE, next to\n"+
		"its .proto file as a .binpb file")
	flag.Parse()
	s.SetGoPackage(*goPackage)
	s.SetWriteDescriptors(*debug)
	s.SetRenameFiles(*rename)
	args := flag.Args()
	if *output == "" || len(args) != 1 {
		log.Fatal("proto-extract -output DEST [-go_package prefix [-rename]] [-option name=value]... [-debug] SOURCE")
	}
	if *rename && *goPackage == "" {
		log.Fatal("-rename needs -go_package")
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
		// Only well-known files, such as descriptor.proto, which are never
		// written.
		log.Fatal("no descriptors to write")
	}

	fmt.Printf("wrote %d descriptors to %s\n", written, *output)
}
