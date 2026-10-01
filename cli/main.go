package main

import (
	"os"

	"heall/cmd"
)

func main() {
	os.Exit(cmd.ExitCode(cmd.Execute()))
}
