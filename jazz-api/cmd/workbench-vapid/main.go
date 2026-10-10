// Command workbench-vapid creates a VAPID key pair for Workbench Web Push and
// stores the private key straight in Secret Manager, never on disk or in the
// terminal scrollback. It prints only the public key.
//
//	go run ./cmd/workbench-vapid | ...           # prints both as env lines (local testing only)
//	go run ./cmd/workbench-vapid -secret workbench-vapid-private-key -project parabolio-prod
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"

	webpush "github.com/SherClockHolmes/webpush-go"
)

func main() {
	secret := flag.String("secret", "", "Secret Manager secret to create with the private key")
	project := flag.String("project", "", "GCP project for -secret")
	flag.Parse()
	private, public, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if *secret == "" {
		fmt.Printf("WORKBENCH_VAPID_PUBLIC_KEY=%s\nWORKBENCH_VAPID_PRIVATE_KEY=%s\n", public, private)
		return
	}
	args := []string{"secrets", "create", *secret, "--data-file=-", "--replication-policy=automatic"}
	if *project != "" {
		args = append(args, "--project", *project)
	}
	cmd := exec.Command("gcloud", args...)
	cmd.Stdin = bytes.NewBufferString(private)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "gcloud secrets create failed:", err)
		os.Exit(1)
	}
	fmt.Printf("WORKBENCH_VAPID_PUBLIC_KEY=%s\n", public)
}
