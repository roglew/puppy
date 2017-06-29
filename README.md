The Puppy Proxy
===============

What is this?
-------------
Puppy is a golang library that can be used to create proxies to intercept and modify HTTP and websocket messages that pass through it. Puppy itself does not provide any interactive interface, it provides an API to do proxy things in go. If you want a useful tool that uses Puppy, try [Pappy](https://github.com/roglew/pappy-proxy).

Puppy was originally aimed to be a starting point to write a tool similar to [Burp Suite](https://portswigger.net/burp/) and to provide a base for writing other HTTP proxy software.

Features
--------

* Intercept and modify any HTTP messages passing through the proxy
* Websocket support
* Use custom CA certificate to strip TLS from HTTPS connections
* Built in IPC API
* Support for transparent request redirection
* Built in support for writing messages to SQLite database
* Flexible history search

Example
-------

The following example creates a simple proxy which listens on port 8080. In order to send HTTPS traffic through the proxy, you must add the generated server.pem certificate as a CA to your browser.

```go
package main

import (
	"fmt"
	"net"
	"os"
	"path"
	"puppy"
)

func checkerr(err error) {
    if err != nil {
        panic(err)
    }
}

func main() {
    // Create the proxy without a logger
    iproxy := puppy.NewInterceptingProxy(nil)

    // Load the CA certs
    ex, err := os.Executable()
    checkerr(err)
    certFile := path.Dir(ex) + "/server.pem"
    pkeyFile := path.Dir(ex) + "/server.key"
	err = iproxy.LoadCACertificates(certFile, pkeyFile)
    if err != nil {
        // Try generating the certs in case they're missing
        _, err := puppy.GenerateCACertsToDisk(certFile, pkeyFile)
        checkerr(err)
	    err = iproxy.LoadCACertificates(certFile, pkeyFile)
        checkerr(err)
    }

    // Listen on port 8080
    listener, err := net.Listen("tcp", "127.0.0.1:8080")
    checkerr(err)
    iproxy.AddListener(listener)

    // Wait for exit
    fmt.Println("Proxy is running on localhost:8080")
	select {}
}
```

Next, we will demonstrate editing messages by turning the proxy into a cloud2butt proxy which will replace every instance of the word "cloud" with the word "butt". This is done by writing a function that takes in a request and a response and returns a new response then adding it to the proxy:

```go
package main

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"path"
	"puppy"
)

func checkerr(err error) {
	if err != nil {
		panic(err)
	}
}

func main() {
	// Create the proxy without a logger
	iproxy := puppy.NewInterceptingProxy(nil)

	// Load the CA certs
	ex, err := os.Executable()
	checkerr(err)
	certFile := path.Dir(ex) + "/server.pem"
	pkeyFile := path.Dir(ex) + "/server.key"
	err = iproxy.LoadCACertificates(certFile, pkeyFile)
	if err != nil {
		// Try generating the certs in case they're missing
		_, err := puppy.GenerateCACertsToDisk(certFile, pkeyFile)
		checkerr(err)
		err = iproxy.LoadCACertificates(certFile, pkeyFile)
		checkerr(err)
	}

	// Cloud2Butt interceptor
	var cloud2butt = func(req *puppy.ProxyRequest, rsp *puppy.ProxyResponse) (*puppy.ProxyResponse, error) {
		newBody := rsp.BodyBytes()
		newBody = bytes.Replace(newBody, []byte("cloud"), []byte("butt"), -1)
		newBody = bytes.Replace(newBody, []byte("Cloud"), []byte("Butt"), -1)
		rsp.SetBodyBytes(newBody)
		return rsp, nil
	}
	iproxy.AddRspInterceptor(cloud2butt)

	// Listen on port 8080
	listener, err := net.Listen("tcp", "127.0.0.1:8080")
	checkerr(err)
	iproxy.AddListener(listener)

	// Wait for exit
	fmt.Println("Proxy is running on localhost:8080")
	select {}
}
```

For more information, check out the documentation.