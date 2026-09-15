// Command remotessh runs a remote command over SSH using an OpenSSH private key.
package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

func main() {
	identity := ""
	args := os.Args[1:]
	for len(args) > 0 {
		if args[0] == "-i" && len(args) >= 2 {
			identity = args[1]
			args = args[2:]
			continue
		}
		break
	}
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: remotessh [-i keyfile] user@host remote-command...")
		os.Exit(2)
	}
	target := args[0]
	remoteCmd := strings.Join(args[1:], " ")

	user, host, ok := strings.Cut(target, "@")
	if !ok || user == "" || host == "" {
		fmt.Fprintf(os.Stderr, "invalid target %q (want user@host)\n", target)
		os.Exit(2)
	}
	if !strings.Contains(host, ":") {
		host += ":22"
	}

	signer, err := loadSigner(identity)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load key: %v\n", err)
		os.Exit(1)
	}

	cfg := &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         30 * time.Second,
	}
	client, err := ssh.Dial("tcp", host, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ssh dial: %v\n", err)
		os.Exit(1)
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ssh session: %v\n", err)
		os.Exit(1)
	}
	defer session.Close()

	stdin, _ := session.StdinPipe()
	session.Stdout = os.Stdout
	session.Stderr = os.Stderr

	if err := session.Start(remoteCmd); err != nil {
		fmt.Fprintf(os.Stderr, "ssh start: %v\n", err)
		os.Exit(1)
	}
	go func() {
		_, _ = io.Copy(stdin, os.Stdin)
		_ = stdin.Close()
	}()
	if err := session.Wait(); err != nil {
		if ee, ok := err.(*ssh.ExitError); ok {
			os.Exit(ee.ExitStatus())
		}
		fmt.Fprintf(os.Stderr, "ssh wait: %v\n", err)
		os.Exit(1)
	}
}

func loadSigner(identity string) (ssh.Signer, error) {
	var pem []byte
	var err error
	if identity != "" {
		pem, err = os.ReadFile(identity)
	} else if v := strings.TrimSpace(os.Getenv("PREVIEW_PROXY_SSH_KEY")); v != "" {
		pem = []byte(v)
	} else {
		return nil, fmt.Errorf("set -i keyfile or PREVIEW_PROXY_SSH_KEY")
	}
	if err != nil {
		return nil, err
	}
	return ssh.ParsePrivateKey(pem)
}
