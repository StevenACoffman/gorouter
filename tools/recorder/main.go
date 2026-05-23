// Package main implements a transparent recording proxy for capturing Apollo Router
// → subgraph traffic. Run one instance per subgraph.
//
// Usage:
//
//	go run ./tools/recorder \
//	  --upstream http://localhost:4002 \
//	  --listen :5002 \
//	  --name USERS \
//	  --out /tmp/golden-record/proxy
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"
)

var reqCounter atomic.Int64

func main() {
	upstream := flag.String("upstream", "", "upstream subgraph URL (required)")
	listen   := flag.String("listen", ":5000", "listen address")
	name     := flag.String("name", "SUBGRAPH", "subgraph enum name for filenames")
	outDir   := flag.String("out", "/tmp/recorder", "output directory")
	flag.Parse()

	if *upstream == "" {
		log.Fatal("--upstream is required")
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		log.Fatalf("mkdir %s: %v", *outDir, err)
	}

	target, err := url.Parse(*upstream)
	if err != nil {
		log.Fatalf("parse upstream: %v", err)
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		n := reqCounter.Add(1)

		reqBody, _ := io.ReadAll(r.Body)

		// Forward to upstream.
		fwdReq, err := http.NewRequestWithContext(r.Context(), r.Method,
			target.String()+r.RequestURI, bytes.NewReader(reqBody))
		if err != nil {
			http.Error(w, "proxy error", http.StatusBadGateway)
			return
		}
		for k, vv := range r.Header {
			for _, v := range vv {
				fwdReq.Header.Add(k, v)
			}
		}

		resp, err := http.DefaultClient.Do(fwdReq)
		if err != nil {
			http.Error(w, fmt.Sprintf("upstream error: %v", err), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		respBody, _ := io.ReadAll(resp.Body)

		// Copy response to caller.
		for k, vv := range resp.Header {
			for _, v := range vv {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(respBody)

		// Write record file.
		record := map[string]any{
			"subgraph":  *name,
			"timestamp": time.Now().Format(time.RFC3339Nano),
			"request": map[string]any{
				"method": r.Method,
				"body":   jsonOrString(reqBody),
			},
			"response": map[string]any{
				"status": resp.StatusCode,
				"body":   jsonOrString(respBody),
			},
		}
		path := filepath.Join(*outDir,
			fmt.Sprintf("%s_%04d.json", *name, n))
		data, _ := json.MarshalIndent(record, "", "  ")
		if err := os.WriteFile(path, data, 0o644); err != nil {
			log.Printf("write %s: %v", path, err)
		}
	})

	log.Printf("recording proxy for %s on %s → %s", *name, *listen, *upstream)
	log.Fatal(http.ListenAndServe(*listen, nil))
}

func jsonOrString(b []byte) any {
	var v any
	if json.Unmarshal(b, &v) == nil {
		return v
	}
	return string(b)
}
