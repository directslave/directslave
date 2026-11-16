package main

import (
	"crypto/tls"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
)

type keypairReloader struct {
	certMu   sync.RWMutex
	cert     *tls.Certificate
	certPath string
	keyPath  string
}

func NewKeypairReloader(certPath, keyPath string) (*keypairReloader, error) {
	result := &keypairReloader{
		certPath: certPath,
		keyPath:  keyPath,
	}
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, err
	}
	result.cert = &cert
	go func() {
		c := make(chan os.Signal, 1)
		signal.Notify(c, syscall.SIGHUP)
		for range c {

			LogError.Printf("RELOAD: reloading TLS certificate and key from %q and %q", certPath, keyPath)

			if Config.debug == "1" || Config.background == "1" {
				fmt.Printf("RELOAD: reloading TLS certificate and key from %q and %q\n", certPath, keyPath)
			}

			if err := result.maybeReload(); err != nil {

				LogError.Printf("RELOAD: Keeping old TLS certificate because the new one could not be loaded: %v", err)

				if Config.debug == "1" || Config.background == "1" {
					fmt.Printf("RELOAD: Keeping old TLS certificate because the new one could not be loaded: %v\n", err)

				}

			}
		}
	}()
	return result, nil
}

func (kpr *keypairReloader) maybeReload() error {
	newCert, err := tls.LoadX509KeyPair(kpr.certPath, kpr.keyPath)
	if err != nil {
		return err
	}
	kpr.certMu.Lock()
	defer kpr.certMu.Unlock()
	kpr.cert = &newCert
	return nil
}

func (kpr *keypairReloader) GetCertificateFunc() func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	return func(clientHello *tls.ClientHelloInfo) (*tls.Certificate, error) {
		kpr.certMu.RLock()
		defer kpr.certMu.RUnlock()
		return kpr.cert, nil
	}
}
