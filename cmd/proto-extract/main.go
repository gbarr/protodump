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
		// Descriptors that could not be added are reported, but the files
		// that were found are still dumped.
		if _, ok := errors.AsType[scanner.Errors](err); !ok {
			log.Fatal(err)
		}
		log.Print(err)
	}

	found := s.Files().NumFiles()
	if found == 0 {
		log.Fatal("no descriptors found")
	}

	if err := s.WriteFiles(*output); err != nil {
		log.Fatal(err)
	}

	fmt.Printf("wrote %d descriptors to %s\n", found, *output)
}
