package main

import (
	"os"

	"disc-image-studio/internal/dvd"
)

func main() {
	os.Exit(dvd.RunCommand(os.Args[1:]))
}
