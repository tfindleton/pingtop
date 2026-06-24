package main

import (
	"os"

	"github.com/tfindleton/pingtop/internal/app"
)

func main() {
	os.Exit(app.Run(os.Args[1:]))
}
