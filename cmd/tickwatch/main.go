package main

import (
	"fmt"
	"os"
)

var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = runServe(os.Args[2:])
	case "add":
		err = runAdd(os.Args[2:])
	case "list":
		err = runList(os.Args[2:])
	case "migrate":
		err = runMigrate(os.Args[2:])
	case "token":
		err = runToken(os.Args[2:])
	case "version":
		fmt.Printf("tickwatch %s\n", version)
	case "healthcheck":
		err = runHealthcheck(os.Args[2:])
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Print(`tickwatch - uptime and cron monitoring

Usage:
  tickwatch serve [--config path]            run the server
  tickwatch add <type> <target> [flags]      create a monitor
  tickwatch list                             list monitors
  tickwatch migrate                          apply database migrations
  tickwatch token add <name>                 create an api token
  tickwatch token list                       list api tokens
  tickwatch token delete <id>                delete an api token
  tickwatch healthcheck <url>                check a healthz endpoint
  tickwatch version                          print version

Monitor types:
  http <url>            check an http endpoint
  tcp <host:port>       check a tcp port
  ping <host>           icmp ping (falls back to tcp)
  heartbeat             cron monitor, ping url printed after creation

Add flags:
  --interval seconds    default 60, minimum 20
  --name value          default derived from target
`)
}
