package main

import (
	"flag"
	"log"
	"net/http"
	"os"

	_ "github.com/motemen/go-loghttp/global"

	"github.com/motemen/go-prchecklist/lib/gateway"
	"github.com/motemen/go-prchecklist/lib/repository"
	"github.com/motemen/go-prchecklist/lib/usecase"
	"github.com/motemen/go-prchecklist/lib/web"
)

var (
	datasource = getenv("PRCHECKLIST_DATASOURCE", "bolt:./prchecklist.db")
	addr       string
)

func getenv(key, def string) string {
	v := os.Getenv(key)
	if v != "" {
		return v
	}

	return def
}

func init() {
	flag.StringVar(&datasource, "datasource", datasource, "database source name")
	flag.StringVar(&addr, "listen", "localhost:8080", "`address` to listen")
}

func main() {
	flag.Parse()

	coreRepo, err := repository.NewCore(datasource)
	if err != nil {
		log.Fatal(err)
	}

	github, err := gateway.NewGitHub()
	if err != nil {
		log.Fatal(err)
	}

	app := usecase.New(github, coreRepo)
	w := web.New(app, github)

	log.Printf("prchecklist starting at %s", addr)

	err = http.ListenAndServe(addr, w.Handler())
	log.Fatal(err)
}
