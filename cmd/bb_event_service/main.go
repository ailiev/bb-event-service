package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	_ "net/http/pprof"

	"github.com/buildbarn/bb-storage/pkg/ac"
	"github.com/buildbarn/bb-storage/pkg/blobstore/configuration"
	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/grpc-ecosystem/go-grpc-prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	build "google.golang.org/genproto/googleapis/devtools/build/v1"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

func makeCreds(certFile * string, keyFile * string) (credentials.TransportCredentials, error) {
	if *certFile == "" && *keyFile == "" {
		// No TLS
		return nil, nil
	}
	cert, err := tls.LoadX509KeyPair(*certFile, *keyFile)
	if err != nil {
		return nil, fmt.Errorf("Can't load X509 certificate and keypair '%q', '%q': %s", certFile, keyFile, err)
	}
	cfg := &tls.Config{Certificates: []tls.Certificate{cert}}
	creds := credentials.NewTLS(cfg)
	return creds, nil
}

func main() {
	var (
		blobstoreConfig  = flag.String("blobstore-config", "/config/blobstore.conf", "Configuration for blob storage")
		webListenAddress = flag.String("web.listen-address", ":80", "Port on which to expose metrics")
		certFile         = flag.String("cert-file", "", "TLS certificate file")
		keyFile          = flag.String("key-file", "", "TLS key file")
	)
	flag.Parse()

	err := util.UseBinaryLogTempFileSink()
	if err != nil {
		log.Fatalf("Failed to UseBinaryLogTempFileSink: %v", err)
	}

	// Storage access.
	contentAddressableStorage, actionCache, err := configuration.CreateBlobAccessObjectsFromConfig(*blobstoreConfig)
	if err != nil {
		log.Fatal("Failed to create blob access: ", err)
	}

	// Web server for metrics and profiling.
	http.Handle("/metrics", promhttp.Handler())
	go func() {
		log.Fatal(http.ListenAndServe(*webListenAddress, nil))
	}()

	// RPC server with optional TLS.
	opts := []grpc.ServerOption {
		grpc.StreamInterceptor(grpc_prometheus.StreamServerInterceptor),
		grpc.UnaryInterceptor(grpc_prometheus.UnaryServerInterceptor),
	}
	creds, err := makeCreds(certFile, keyFile)
	if err != nil {
		log.Fatal("Loading TLS materials failed: ", err)
	} else if creds != nil {
		opts = append(opts, grpc.Creds(creds))
		log.Print("Listening with TLS")
	}
	s := grpc.NewServer(opts...)

	build.RegisterPublishBuildEventServer(s, &buildEventServer{
		instanceName:              "bb-event-service",
		contentAddressableStorage: contentAddressableStorage,
		actionCache:               ac.NewBlobAccessActionCache(actionCache),

		streams: map[string]*streamState{},
	})
	grpc_prometheus.EnableHandlingTimeHistogram()
	grpc_prometheus.Register(s)
	sock, err := net.Listen("tcp", ":8983")
	if err != nil {
		log.Fatal("Failed to create listening socket: ", err)
	}
	if err := s.Serve(sock); err != nil {
		log.Fatal("Failed to serve RPC server: ", err)
	}
}
