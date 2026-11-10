// Command tern is the single-binary backup and sync node.
package main

import (
	"fmt"
	"os"
)

var version = "dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Println("tern", version)
		return
	}
}
