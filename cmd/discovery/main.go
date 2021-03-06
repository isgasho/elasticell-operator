package main

import (
	"flag"
	"net/http"
	_ "net/http/pprof"
	"os"
	"time"

	"github.com/deepfabric/elasticell-operator/pkg/client/clientset/versioned"
	"github.com/deepfabric/elasticell-operator/pkg/discovery/server"
	"github.com/deepfabric/elasticell-operator/version"
	"github.com/golang/glog"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apiserver/pkg/util/logs"
	"k8s.io/client-go/rest"
)

var (
	printVersion bool
	port         int
)

func init() {
	flag.BoolVar(&printVersion, "V", false, "Show version and quit")
	flag.BoolVar(&printVersion, "version", false, "Show version and quit")
	flag.IntVar(&port, "port", 10261, "The port that the pd discovery's http service runs on (default 10261)")
	flag.Parse()
}

func main() {
	if printVersion {
		version.PrintVersionInfo()
		os.Exit(0)
	}
	version.LogVersionInfo()

	logs.InitLogs()
	defer logs.FlushLogs()

	cfg, err := rest.InClusterConfig()
	if err != nil {
		glog.Fatalf("failed to get config: %v", err)
	}
	cli, err := versioned.NewForConfig(cfg)
	if err != nil {
		glog.Fatalf("failed to create Clientset: %v", err)
	}

	go wait.Forever(func() {
		server.StartServer(cli, port)
	}, 5*time.Second)
	glog.Fatal(http.ListenAndServe(":6060", nil))
}
