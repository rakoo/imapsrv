package main

import "github.com/rakoo/unpeu"

func main() {
	srv := unpeu.NewServer(
		unpeu.ListenOption("127.0.0.1:1143"),
		unpeu.StoreOption(&unpeu.NotmuchMailstore{}),
	)
	srv.Start()
}
