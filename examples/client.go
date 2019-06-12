package main

import (
	"fmt"
	"os"

	bitcoincash "github.com/BubbaJoe/spvwallet-cash"
	"github.com/BubbaJoe/spvwallet-cash/db"
	"github.com/gcash/bchd/chaincfg"
	"github.com/op/go-logging"
)

func main() {
	// Create a new config
	config := bitcoincash.NewDefaultConfig()

	// Make the logging a little prettier
	backend := logging.NewLogBackend(os.Stdout, "", 0)
	formatter := logging.MustStringFormatter(`%{color:reset}%{color}%{time:15:04:05.000} [%{shortfunc}] [%{level}] %{message}`)
	stdoutFormatter := logging.NewBackendFormatter(backend, formatter)
	config.Logger = logging.MultiLogger(stdoutFormatter)

	// Use testnet
	config.Params = &chaincfg.MainNetParams

	// Select wallet datastore
	sqliteDatastore, _ := db.Create(config.RepoPath)
	config.DB = sqliteDatastore

	// Create the wallet
	wallet, err := bitcoincash.NewSPVWallet(config)
	if err != nil {
		fmt.Println(err)
		return
	}

	// Start it!
	go wallet.Start()
	<-make(chan os.Signal)
}
