package main

import (
	"crypto/tls"
	"net/http"
	"time"
)

func newClient(insecure bool, timeout time.Duration) *http.Client {
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy:           http.ProxyFromEnvironment,
			TLSClientConfig: &tls.Config{InsecureSkipVerify: insecure},
		},
	}
}
