package puppy

import (
	"encoding/pem"
	"html/template"
	"net/http"
	"strings"
)


func responseHeaders(w http.ResponseWriter) {
	w.Header().Set("Connection", "close")
	w.Header().Set("Cache-control", "no-cache")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Cache-control", "no-store")
	w.Header().Set("X-Frame-Options", "DENY")
}

// Generate a proxy-compatible web handler that allows users to download certificates and view responses stored in the storage used by the proxyin the browser
func CreateWebUIHandler() ProxyWebUIHandler {
	var masterSrc string = `
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
	var masterTpl *template.Template

	var homeSrc string = `
	{{define "title"}}Puppy Home{{end}}
	{{define "body"}}
		<p>Welcome to Puppy<p>
		<ul>
		<li><a href="/certs">Download CA certificate</a></li>
		</ul>
	{{end}}
	`
	var homeTpl *template.Template

	var certsSrc string = `
	{{define "title"}}CA Certificate{{end}}
	{{define "body"}}
		<p>Downlad this CA cert and add it to your browser to intercept HTTPS messages<p>
		<p><a href="/certs/download">Download</p>
	{{end}}
	`
	var certsTpl *template.Template

	var rspviewSrc string = `
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
	var rspviewTpl *template.Template

	var err error
	masterTpl, err = template.New("master").Parse(masterSrc)
	if err != nil {
		panic(err)
	}

	homeTpl, err = template.Must(masterTpl.Clone()).Parse(homeSrc)
	if err != nil {
		panic(err)
	}

	certsTpl, err = template.Must(masterTpl.Clone()).Parse(certsSrc)
	if err != nil {
		panic(err)
	}

	rspviewTpl, err = template.Must(masterTpl.Clone()).Parse(rspviewSrc)
	if err != nil {
		panic(err)
	}

	var WebUIRootHandler = func(w http.ResponseWriter, r *http.Request, iproxy *InterceptingProxy) {
		err := homeTpl.Execute(w, nil)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	var WebUICertsHandler = func(w http.ResponseWriter, r *http.Request, iproxy *InterceptingProxy, path []string) {
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
		err := certsTpl.Execute(w, nil)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	var viewResponseHeaders = func(w http.ResponseWriter) {
		w.Header().Del("Cookie")
	}

	var WebUIRspHandler = func(w http.ResponseWriter, r *http.Request, iproxy *InterceptingProxy, path []string) {
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
		err := rspviewTpl.Execute(w, nil)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	return func(w http.ResponseWriter, r *http.Request, iproxy *InterceptingProxy) {
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

}
