package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
)

func main() {
	port := flag.Int("port", 9999, "port to listen on")
	fail := flag.Bool("fail", false, "return 500 errors")
	flag.Parse()

	http.HandleFunc("/webhook", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		fmt.Println("=== Webhook Received ===")
		fmt.Printf("X-Dispatch-Event-ID: %s\n", r.Header.Get("X-Dispatch-Event-ID"))
		fmt.Printf("X-Dispatch-Event-Type: %s\n", r.Header.Get("X-Dispatch-Event-Type"))
		fmt.Printf("X-Dispatch-Signature: %s\n", r.Header.Get("X-Dispatch-Signature"))
		fmt.Printf("Body: %s\n", string(body))
		fmt.Println("========================")

		if *fail {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("simulated failure"))
			fmt.Println("-> Responded with 500")
		} else {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("OK"))
			fmt.Println("-> Responded with 200")
		}
		fmt.Println()
	})

	addr := fmt.Sprintf(":%d", *port)
	fmt.Printf("Test webhook server listening on %s (fail=%v)\n", addr, *fail)
	log.Fatal(http.ListenAndServe(addr, nil))
}
