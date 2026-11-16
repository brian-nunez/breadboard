// Command probe is the test app of the sidecar integration tests.
//
//	probe serve [ADDR]       HTTP on ADDR, else $BB_LISTEN_ADDR, else :8080;
//	                         answers with the instance name and the protocol
//	                         the request arrived on
//	probe get URL            one GET; prints the body, exits 1 unless it is a 200
//	probe hits URL N         N GETs; prints how many each instance answered
//	probe dial HOST:PORT     exits 0 if a TCP connection opens
//	probe asuid UID ARGS...  switches to user UID, then runs probe ARGS
package main

import (
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		log.Fatal("usage: probe serve | get URL | hits URL N")
	}
	switch os.Args[1] {
	case "serve":
		addr := os.Getenv("BB_LISTEN_ADDR")
		if len(os.Args) > 2 {
			addr = os.Args[2]
		}
		if addr == "" {
			addr = ":8080"
		}
		serve(addr)
	case "dial":
		c, err := net.DialTimeout("tcp", os.Args[2], 5*time.Second)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		c.Close()
		fmt.Println("connected")
	case "asuid":
		uid, _ := strconv.Atoi(os.Args[2])
		if err := syscall.Setgid(uid); err != nil {
			fmt.Fprintln(os.Stderr, "setgid:", err)
			os.Exit(1)
		}
		if err := syscall.Setuid(uid); err != nil {
			fmt.Fprintln(os.Stderr, "setuid:", err)
			os.Exit(1)
		}
		os.Args = append([]string{os.Args[0]}, os.Args[3:]...)
		main()
	case "get":
		body, err := get(os.Args[2])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Print(body)
	case "hits":
		n, _ := strconv.Atoi(os.Args[3])
		seen := map[string]int{}
		for range n {
			body, err := get(os.Args[2])
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			seen[strings.Fields(body)[0]]++
		}
		var keys []string
		for k := range seen {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Printf("%s %d\n", k, seen[k])
		}
	default:
		log.Fatalf("unknown command %q", os.Args[1])
	}
}

func serve(addr string) {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		proto := r.Header.Get("X-Forwarded-Proto")
		if proto == "" {
			proto = "http"
		}
		line := fmt.Sprintf("instance=%s proto=%s host=%s", os.Getenv("BB_SERVICE"), proto, r.Host)
		log.Print("served " + line)
		fmt.Fprintln(w, line)
	})
	log.Printf("listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}

func get(url string) (string, error) {
	c := &http.Client{Timeout: 5 * time.Second}
	resp, err := c.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s: %s: %s", url, resp.Status, body)
	}
	return string(body), nil
}
