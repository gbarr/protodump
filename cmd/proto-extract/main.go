package main

import (
	"errors"
	"flag"
	"fmt"
	"log"

	"github.com/gbarr/protodump/scanner"
)

func main() {
	output := flag.String("output", "", "directory to output to")
	flag.Parse()
	args := flag.Args()
	if *output == "" || len(args) != 1 {
		log.Fatal("proto-extract -output DEST SOURCE")
	}

	s := scanner.New(nil)
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
