// Command httpsrv is the fixture memci's end-to-end test measures. The base
// and head copies differ the way a real change would: head allocates more per
// operation and links in more code.
package main

import (
	"net/http"
)

func main() {
	http.HandleFunc("/", index)
	http.ListenAndServe(":8080", nil)
}

func index(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte(greet(r.URL.Path)))
}

// greet builds the response body. The base version concatenates, which the Go
// compiler can do with a single allocation.
func greet(name string) string {
	return "hello " + name
}
