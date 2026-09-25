package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

const (
	servingAt      = "127.0.0.1:443"
	servingTimeout = 10 * time.Second
	exitUnservable = 6
	edgeAnswerCap  = 1 << 12
)

func loopback(verb string, argv []string, errs io.Writer) (string, string, bool) {
	flags := flag.NewFlagSet(verb, flag.ContinueOnError)
	flags.SetOutput(errs)
	at := flags.String("at", servingAt, "")
	if err := flags.Parse(argv); err != nil || flags.NArg() != 1 || flags.Arg(0) == "" {
		return "", "", false
	}
	return *at, flags.Arg(0), true
}

func handshake(at, hostname string) (*tls.Conn, error) {
	held, err := net.DialTimeout("tcp", at, servingTimeout)
	if err != nil {
		return nil, err
	}
	spoken := tls.Client(held, &tls.Config{ServerName: hostname, InsecureSkipVerify: true})
	if err := spoken.SetDeadline(time.Now().Add(servingTimeout)); err != nil {
		_ = spoken.Close()
		return nil, err
	}
	if err := spoken.Handshake(); err != nil {
		_ = spoken.Close()
		return nil, err
	}
	return spoken, nil
}

func leaf(argv []string, out, errs io.Writer) int {
	at, hostname, ok := loopback("leaf", argv, errs)
	if !ok {
		return usage(errs)
	}
	spoken, err := handshake(at, hostname)
	var dialled *net.OpError
	switch {
	case errors.As(err, &dialled) && dialled.Op == "dial":
		fmt.Fprintf(errs, "%s: nothing answered %s: %v\n", switchboard.Name, at, err)
		return exitRefused
	case err != nil && declined(err):
		fmt.Fprintf(errs, "%s: %s served no certificate for %s: %v\n", switchboard.Name, at, hostname, err)
		return exitNotServingYet
	case err != nil && held(err):
		fmt.Fprintf(errs, "%s: %s held the handshake for %s past %s, still ordering its certificate: %v\n", switchboard.Name, at, hostname, servingTimeout, err)
		return exitNotServingYet
	case err != nil:
		fmt.Fprintf(errs, "%s: tls handshake for %s failed: %v\n", switchboard.Name, hostname, err)
		return exitUnservable
	}
	defer spoken.Close()
	chain := spoken.ConnectionState().PeerCertificates
	if len(chain) == 0 {
		fmt.Fprintf(errs, "%s: %s completed a handshake for %s and presented no certificate\n", switchboard.Name, at, hostname)
		return exitUnservable
	}
	if err := pem.Encode(out, &pem.Block{Type: "CERTIFICATE", Bytes: chain[0].Raw}); err != nil {
		return refuse(errs, err)
	}
	return 0
}

func probe(argv []string, out, errs io.Writer) int {
	at, hostname, ok := loopback("probe", argv, errs)
	if !ok {
		return usage(errs)
	}
	spoken, err := handshake(at, hostname)
	if err != nil {
		fmt.Fprintf(errs, "%s answered nothing over tls at %s: %v\n", hostname, at, oneLine(err))
		return exitNotServingYet
	}
	if err := serves(spoken.ConnectionState().PeerCertificates, hostname, time.Now()); err != nil {
		_ = spoken.Close()
		fmt.Fprintf(errs, "%s at %s: %v\n", hostname, at, err)
		return exitNotServingYet
	}
	used := false
	client := &http.Client{
		Timeout: servingTimeout,
		Transport: &http.Transport{
			DialTLSContext: func(context.Context, string, string) (net.Conn, error) {
				if used {
					return nil, errors.New("the probe asks one question over the handshake it verified")
				}
				used = true
				return spoken, nil
			},
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	defer client.CloseIdleConnections()
	answer, err := client.Get("https://" + hostname + edge.LivenessProbePath)
	if err != nil {
		fmt.Fprintf(errs, "%s answered nothing at %s: %v\n", hostname, at, oneLine(err))
		return exitNotServingYet
	}
	defer answer.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(answer.Body, edgeAnswerCap))
	answered := strings.TrimSpace(answer.Header.Get(edge.HeaderEdge))
	heard := answer.Header.Get(switchboard.HeardHeader)
	if heard == "" {
		fmt.Fprintf(errs, "%s answered at %s as %q, and %s never did: route it to %s\n", hostname, at, answered, switchboard.Name, switchboard.Name)
		return exitNotServingYet
	}
	if err := hearing(heard, hostname); err != nil {
		fmt.Fprintf(errs, "%s reaches %s, and %v\n", hostname, switchboard.Name, err)
		return exitNotServingYet
	}
	fmt.Fprintln(out, answered)
	return 0
}

func hearing(heard, hostname string) error {
	said := strings.Fields(heard)
	said = append(said, make([]string, max(0, 3-len(said)))...)
	proto, host, forwarded := said[0], said[1], said[2]
	if proto != "https" {
		return fmt.Errorf("its apps would hear it over %s: set X-Forwarded-Proto to https where your proxy forwards it", proto)
	}
	if !strings.EqualFold(host, hostname) {
		return fmt.Errorf("its apps would hear it as %q: keep the Host header where your proxy forwards it", host)
	}
	if !strings.EqualFold(forwarded, hostname) {
		return fmt.Errorf("its apps would hear it forwarded for %q: set X-Forwarded-Host to the Host asked, or leave it unset, where your proxy forwards it", forwarded)
	}
	return nil
}

func serves(chain []*x509.Certificate, hostname string, now time.Time) error {
	if len(chain) == 0 {
		return fmt.Errorf("the handshake presented no certificate")
	}
	served := chain[0]
	if err := served.VerifyHostname(hostname); err != nil {
		return fmt.Errorf("the certificate served is for %s, not this name", strings.Join(served.DNSNames, ", "))
	}
	if now.Before(served.NotBefore) || now.After(served.NotAfter) {
		return fmt.Errorf("the certificate served holds from %s until %s, and it is %s",
			served.NotBefore.UTC().Format(time.RFC3339), served.NotAfter.UTC().Format(time.RFC3339), now.UTC().Format(time.RFC3339))
	}
	return nil
}

func declined(err error) bool {
	var refused *net.OpError
	return errors.As(err, &refused) && refused.Op == "remote error"
}

func held(err error) bool {
	var stalled net.Error
	return errors.As(err, &stalled) && stalled.Timeout()
}

func oneLine(err error) string { return strings.Join(strings.Fields(err.Error()), " ") }
