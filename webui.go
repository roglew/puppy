package main

import (
	"encoding/pem"
	"html/template"
	"net/http"
	"strings"
)

// Page template
var MASTER_SRC string = `
<html>
<head>
<title>{{block "title" .}}Puppy Proxy{{end}}</title>
{{block "head" .}}{{end}}
</head>
<body>
{{block "body" .}}{{end}}
</body>
</html>
`
var MASTER_TPL *template.Template

// Page sources
var HOME_SRC string = `
{{define "title"}}Puppy Home{{end}}
{{define "body"}}
	<p>Welcome to Puppy<p>
	<ul>
	<li><a href="/certs">Download CA certificate</a></li>
	</ul>
{{end}}
`
var HOME_TPL *template.Template

var CERTS_SRC string = `
{{define "title"}}CA Certificate{{end}}
{{define "body"}}
	<p>Downlad this CA cert and add it to your browser to intercept HTTPS messages<p>
	<p><a href="/certs/download">Download</p>
{{end}}
`
var CERTS_TPL *template.Template

var RSPVIEW_SRC string = `
{{define "title"}}Response Viewer{{end}}
{{define "head"}}
	<script>
	function ViewResponse() {
		rspid = document.getElementById("rspid").value
		window.location.href = "/rsp/" + rspid
	}
	</script>
{{end}}
{{define "body"}}
	<p>Enter a response ID below to view it in the browser<p>
	<input type="text" id="rspid"></input><input type="button" onclick="ViewResponse()" value="Go!"></input>
{{end}}
`
var RSPVIEW_TPL *template.Template

func init() {
	var err error
	MASTER_TPL, err = template.New("master").Parse(MASTER_SRC)
	if err != nil {
		panic(err)
	}

	HOME_TPL, err = template.Must(MASTER_TPL.Clone()).Parse(HOME_SRC)
	if err != nil {
		panic(err)
	}

	CERTS_TPL, err = template.Must(MASTER_TPL.Clone()).Parse(CERTS_SRC)
	if err != nil {
		panic(err)
	}

	RSPVIEW_TPL, err = template.Must(MASTER_TPL.Clone()).Parse(RSPVIEW_SRC)
	if err != nil {
		panic(err)
	}
}

func responseHeaders(w http.ResponseWriter) {
	w.Header().Set("Connection", "close")
	w.Header().Set("Cache-control", "no-cache")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Cache-control", "no-store")
	w.Header().Set("X-Frame-Options", "DENY")
}

func WebUIHandler(w http.ResponseWriter, r *http.Request, iproxy *InterceptingProxy) {
	responseHeaders(w)
	parts := strings.Split(r.URL.Path, "/")
	switch parts[1] {
	case "":
		WebUIRootHandler(w, r, iproxy)
	case "certs":
		WebUICertsHandler(w, r, iproxy, parts[2:])
	case "rsp":
		WebUIRspHandler(w, r, iproxy, parts[2:])
	}
}

func WebUIRootHandler(w http.ResponseWriter, r *http.Request, iproxy *InterceptingProxy) {
	err := HOME_TPL.Execute(w, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

func WebUICertsHandler(w http.ResponseWriter, r *http.Request, iproxy *InterceptingProxy, path []string) {
	if len(path) > 0 && path[0] == "download" {
		cert := iproxy.GetCACertificate()
		if cert == nil {
			w.Write([]byte("no active certs to download"))
			return
		}

		pemData := pem.EncodeToMemory(
			&pem.Block{
				Type:  "CERTIFICATE",
				Bytes: cert.Certificate[0],
			},
		)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", "attachment; filename=\"cert.pem\"")
		w.Write(pemData)
		return
	}
	err := CERTS_TPL.Execute(w, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

func viewResponseHeaders(w http.ResponseWriter) {
	w.Header().Del("Cookie")
}

func WebUIRspHandler(w http.ResponseWriter, r *http.Request, iproxy *InterceptingProxy, path []string) {
	if len(path) > 0 {
		reqid := path[0]
		ms := iproxy.GetProxyStorage()
		req, err := ms.LoadRequest(reqid)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		rsp := req.ServerResponse
		for k, v := range rsp.Header {
			for _, vv := range v {
				w.Header().Add(k, vv)
			}
		}
		viewResponseHeaders(w)
		w.WriteHeader(rsp.StatusCode)
		w.Write(rsp.BodyBytes())
		return
	}
	err := RSPVIEW_TPL.Execute(w, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}
