package main

import (
	"log"
	"os"

	"github.com/cloudticon/ct/internal/cli"
)

func main() {
	log.SetFlags(0)
	if err := cli.Execute(); err != nil {
		cli.PrintError(os.Stderr, err)
		os.Exit(1)
	}
}
