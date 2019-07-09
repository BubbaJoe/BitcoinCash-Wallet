package main

import (
	"fmt"
	"log"
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
	datastore, _ := db.Create(config.RepoPath)
	config.DB = datastore

	mnemonic, err := datastore.GetMnemonic()
	if err != nil {
		log.Println("No mnemonic found.. Creating a new private keys")
	} else {
		config.Mnemonic = mnemonic
		creationDate, err := datastore.GetCreationDate()
		if err != nil {
			log.Println("mnemonic has no no creation date...", err)
		} else {
			config.CreationDate = creationDate
		}
	}

	// Create the wallet
	wallet, err := bitcoincash.NewSPVWallet(config)
	if err != nil {
		fmt.Println(err)
		return
	}

	// Start it!
	wallet.Start()
	<-make(chan os.Signal)
}
