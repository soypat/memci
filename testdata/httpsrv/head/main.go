// Command httpsrv is the fixture memci's end-to-end test measures. The base
// and head copies differ the way a real change would: head allocates more per
// operation and links in more code.
package main

import (
	"encoding/json"
	"net/http"
)

func main() {
	http.HandleFunc("/", index)
	http.HandleFunc("/status", status)
	http.ListenAndServe(":8080", nil)
}

func index(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte(greet(r.URL.Path)))
}

// status pulls encoding/json into the binary, which is the size half of the
// synthetic change.
func status(w http.ResponseWriter, r *http.Request) {
	json.NewEncoder(w).Encode(map[string]string{"status": "ok", "path": r.URL.Path})
}

// last retains the most recent buffer. Retaining it is what forces the buffer
// to escape to the heap, so head allocates once for the buffer and once for the
// returned string where base allocates only once.
var last []byte

// greet builds the response body. The head version assembles it in a buffer it
// keeps, which costs one more allocation per call than base's concatenation.
func greet(name string) string {
	b := make([]byte, 0, 64)
	b = append(b, "hello "...)
	b = append(b, name...)
	b = append(b, ", welcome"...)
	last = b
	return string(b)
}
