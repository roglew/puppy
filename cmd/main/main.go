package main

import (
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

    "puppy"
)

var logBanner string = `
========================================
PUPPYSTARTEDPUPPYSTARTEDPUPPYSTARTEDPUPP
         .--.             .---.
        /:.  '.         .' ..  '._.---.
       /:::-.  \.-"""-;' .-:::.     .::\
      /::'|  '\/  _ _  \'   '\:'   ::::|
  __.'    |   /  (o|o)  \     ''.   ':/
 /    .:. /   |   ___   |        '---'
|    ::::'   /:  (._.) .:\
\    .='    |:'        :::|
 '""'       \     .-.   ':/
             '---'|I|'---'
jgs               '-'
PUPPYSTARTEDPUPPYSTARTEDPUPPYSTARTEDPUPP
========================================
`

type listenArg struct {
	Type string
	Addr string
}

func quitErr(msg string) {
	os.Stderr.WriteString(msg)
	os.Stderr.WriteString("\n")
	os.Exit(1)
}

func checkErr(err error) {
	if err != nil {
		quitErr(err.Error())
	}
}

func parseListenString(lstr string) (*listenArg, error) {
	args := strings.SplitN(lstr, ":", 2)
	if len(args) != 2 {
		return nil, errors.New("invalid listener. Must be in the form of \"tye:addr\"")
	}
	argStruct := &listenArg{
		Type: strings.ToLower(args[0]),
		Addr: args[1],
	}
	if argStruct.Type != "tcp" && argStruct.Type != "unix" {
		return nil, fmt.Errorf("invalid listener type: %s", argStruct.Type)
	}
	return argStruct, nil
}

func unixAddr() string {
	return fmt.Sprintf("%s/proxy.%d.%d.sock", os.TempDir(), os.Getpid(), time.Now().UnixNano())
}

var mln net.Listener
var logger *log.Logger

func cleanup() {
	if mln != nil {
		mln.Close()
	}
}

var MainLogger *log.Logger

func main() {
	defer cleanup()
	// Handle signals
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, os.Interrupt, os.Kill, syscall.SIGTERM)
	go func() {
		<-sigc
		if logger != nil {
			logger.Println("Caught signal. Cleaning up.")
		}
		cleanup()
		os.Exit(0)
	}()

	msgListenStr := flag.String("msglisten", "", "Listener for the message handler. Examples: \"tcp::8080\", \"tcp:127.0.0.1:8080\", \"unix:/tmp/foobar\"")
	autoListen := flag.Bool("msgauto", false, "Automatically pick and open a unix or tcp socket for the message listener")
	debugFlag := flag.Bool("dbg", false, "Enable debug logging")
	flag.Parse()

	if *debugFlag {
		logfile, err := os.OpenFile("log.log", os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
		checkErr(err)
		logger = log.New(logfile, "[*] ", log.Lshortfile)
	} else {
		logger = log.New(ioutil.Discard, "[*] ", log.Lshortfile)
		log.SetFlags(0)
	}
	MainLogger = logger

	// Parse arguments to structs
	if *msgListenStr == "" && *autoListen == false {
		quitErr("message listener address or `--msgauto` required")
	}
	if *msgListenStr != "" && *autoListen == true {
		quitErr("only one of listener address or `--msgauto` can be used")
	}

	// Create the message listener
	var listenStr string
	if *msgListenStr != "" {
		msgAddr, err := parseListenString(*msgListenStr)
		checkErr(err)
		if msgAddr.Type == "tcp" {
			var err error
			mln, err = net.Listen("tcp", msgAddr.Addr)
			checkErr(err)
		} else if msgAddr.Type == "unix" {
			var err error
			mln, err = net.Listen("unix", msgAddr.Addr)
			checkErr(err)
		} else {
			quitErr("unsupported listener type:" + msgAddr.Type)
		}
		listenStr = fmt.Sprintf("%s:%s", msgAddr.Type, msgAddr.Addr)
	} else {
		fpath := unixAddr()
		ulisten, err := net.Listen("unix", fpath)
		if err == nil {
			mln = ulisten
			listenStr = fmt.Sprintf("unix:%s", fpath)
		} else {
			tcplisten, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				quitErr("unable to open any messaging ports")
			}
			mln = tcplisten
			listenStr = fmt.Sprintf("tcp:%s", tcplisten.Addr().String())
		}
	}

	// Set up the intercepting proxy
	iproxy := puppy.NewInterceptingProxy(logger)
	iproxy.AddHTTPHandler("puppy", puppy.CreateWebUIHandler())

	// Create a message server and have it serve for the iproxy
	mserv := puppy.NewProxyMessageListener(logger, iproxy)
	logger.Print(logBanner)
	fmt.Println(listenStr)
	mserv.Serve(mln) // serve until killed
}
