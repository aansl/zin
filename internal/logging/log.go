// Package logging renders colored request logs on stderr.
package logging

import (
	"fmt"
	"net/http"
	"os"
	"time"
)

var (
	reset  = "\033[0m"
	red    = "\033[31m"
	green  = "\033[32m"
	yellow = "\033[33m"
	blue   = "\033[34m"
	cyan   = "\033[36m"
)

func LogRequest(req *http.Request, status int, backendPath string) {
	fmt.Fprintf(os.Stderr, "%s[%s]%s %s%s%s %s%s%s %s%d%s -> %s%s%s\n",
		cyan, time.Now().Format("15:04:05"), reset,
		colorForMethod(req.Method), req.Method, reset,
		yellow, req.URL.Path, reset,
		colorForStatus(status), status, reset,
		green, backendPath, reset,
	)
}

func colorForMethod(method string) string {
	switch method {
	case "GET":
		return green
	case "POST":
		return blue
	case "PUT":
		return yellow
	case "PATCH":
		return cyan
	case "DELETE":
		return red
	default:
		return reset
	}
}

func colorForStatus(status int) string {
	if status >= 200 && status < 300 {
		return green
	}
	if status >= 400 {
		return red
	}
	return yellow
}
