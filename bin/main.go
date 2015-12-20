package main

import (
	"log"
	"os"
	"os/signal"
	"runtime/pprof"

	"github.com/rakoo/unpeu"
)

func main() {
	f, err := os.Create("cpu.prof")
	if err != nil {
		log.Fatal(err)
	}

	pprof.StartCPUProfile(f)
	defer func() {
		pprof.StopCPUProfile()
		f.Close()
	}()

	g, err := os.Create("mem.prof")
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		pprof.WriteHeapProfile(g)
		g.Close()
	}()

	srv := unpeu.NewServer(
		unpeu.ListenOption("127.0.0.1:1143"),
		unpeu.StoreOption(unpeu.NewNotmuchMailstore()),
	)
	srv.Start()

	quit := make(chan os.Signal)
	signal.Notify(quit, os.Kill, os.Interrupt)
	<-quit
	srv.Stop()
}
