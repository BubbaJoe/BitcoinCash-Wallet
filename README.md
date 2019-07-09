# Bitcoin Cash Wallet

<img src="https://bitcoin.tax/blog/content/images/2017/08/bitcoincash.png">

This is a fork of https://github.com/cpacia/spvwallet-cash, which is a fork of https://github.com/OpenBazaar/spvwallet modfied for Bitcoin Cash. It includes a fully functional GUI wallet, Go Library and CLI.

It uses stock bchd plus a few cash specific modifications found in the [bchutil](https://github.com/gcash/bchutil) package.

Lightweight p2p SPV wallet and library in Go. It connects directly to the bitcoin p2p network to fetch headers, merkle blocks, and transactions.

Library Usage:
```go
// Create a new config
config := spvwallet.NewDefaultConfig()

// Select network
config.Params = &chaincfg.TestNet3Params

// Select wallet datastore
sqliteDatastore, _ := db.Create(config.RepoPath)
config.DB = sqliteDatastore

// Create the wallet
wallet, _ := spvwallet.NewSPVWallet(config)

// Start it!
go wallet.Start()
```

Easy peasy

The wallet implements the following interface:
```go

type BitcoinWallet interface {

	// Start the wallet
	Start()

	// Return the network parameters
	Params() *chaincfg.Params

	// Returns the type of crytocurrency this wallet implements
	CurrencyCode() string

	// Check if this amount is considered dust
	IsDust(amount int64) bool

	// Get the master private key
	MasterPrivateKey() *hd.ExtendedKey

	// Get the master public key
	MasterPublicKey() *hd.ExtendedKey

	// Get the current address for the given purpose
	CurrentAddress(purpose spvwallet.KeyPurpose) bchutil.Address

	// Returns a fresh address that has never been returned by this function
	NewAddress(purpose spvwallet.KeyPurpose) bchutil.Address

	// Parse the address string and return an address interface
	DecodeAddress(addr string) (bchutil.Address, error)

	// Turn the given output script into an address
	ScriptToAddress(script []byte) (bchutil.Address, error)

	// Turn the given address into an output script
	AddressToScript(addr bchutil.Address) ([]byte, error)

	// Returns if the wallet has the key for the given address
	HasKey(addr bchutil.Address) bool

	// Get the confirmed and unconfirmed balances
	Balance() (confirmed, unconfirmed int64)

	// Returns a list of transactions for this wallet
	Transactions() ([]spvwallet.Txn, error)

	// Get info on a specific transaction
	GetTransaction(txid chainhash.Hash) (spvwallet.Txn, error)

	// Get the height of the blockchain
	ChainTip() uint32

	// Get the current fee per byte
	GetFeePerByte(feeLevel spvwallet.FeeLevel) uint64

	// Send bitcoins to an external wallet
	Spend(amount int64, addr bchutil.Address, feeLevel spvwallet.FeeLevel) (*chainhash.Hash, error)

	// Bump the fee for the given transaction
	BumpFee(txid chainhash.Hash) (*chainhash.Hash, error)

	// Calculates the estimated size of the transaction and returns the total fee for the given feePerByte
	EstimateFee(ins []spvwallet.TransactionInput, outs []spvwallet.TransactionOutput, feePerByte uint64) uint64

	// Build and broadcast a transaction that sweeps all coins from an address. If it is a p2sh multisig, the redeemScript must be included
	SweepAddress(utxos []spvwallet.Utxo, address *bchutil.Address, key *hd.ExtendedKey, redeemScript *[]byte, feeLevel spvwallet.FeeLevel) (*chainhash.Hash, error)

	// Create a signature for a multisig transaction
	CreateMultisigSignature(ins []spvwallet.TransactionInput, outs []spvwallet.TransactionOutput, key *hd.ExtendedKey, redeemScript []byte, feePerByte uint64) ([]spvwallet.Signature, error)

	// Combine signatures and optionally broadcast
	Multisign(ins []spvwallet.TransactionInput, outs []spvwallet.TransactionOutput, sigs1 []spvwallet.Signature, sigs2 []spvwallet.Signature, redeemScript []byte, feePerByte uint64, broadcast bool) ([]byte, error)

	// Generate a multisig script from public keys. If a timeout is included the returned script should be a timelocked escrow which releases using the timeoutKey.
	GenerateMultisigScript(keys []hd.ExtendedKey, threshold int, timeout time.Duration, timeoutKey *hd.ExtendedKey) (addr bchutil.Address, redeemScript []byte, err error)

	// Adds a script to the wallet and get notifications back when coins are received or spent from it
	AddWatchedScript(script []byte) error

	// Adds a script to the wallet and get notifications back when coins are received or spent from it
	RemoveWatchedScript(script []byte) error

	// Adds a callback for incoming transactions. the showEveryTx will not filter, and show all tx's - eturns a callbackId
	AddTransactionListener(showEveryTx bool, callback func(spvwallet.TransactionCallback)) (callbackId int)

	// Removes a transaction listener by the callbackId and returns and error
	RemoveTransactionListener(callbackId int) error

	// Adds a callback for incoming blocks. the showTipOnly will only show blocks with 1 confirmation tx's. Returns a callbackId
	AddBlockListener(showTipOnly bool, callback func(spvwallet.BlockCallback)) (callbackId int)

	// Removes a block listener by the callbackId and returns and error
	RemoveBlockListener(callbackId int) error

	// Use this to re-download merkle blocks in case of missed transactions
	ReSyncBlockchain(time time.Time)

	// Return the number of confirmations and the height for a transaction
	GetConfirmations(txid chainhash.Hash) (confirms, atHeight uint32, err error)

	// Cleanly disconnect from the wallet
	Close()
}
```

To create a wallet binary:
```
make install
```

Usage:
```
Usage:
  spvwallet [OPTIONS] <command>

Help Options:
  -h, --help  Show this help message

Available commands:
  addwatchedscript         add a script to watch
  balance                  get the wallet balance
  bumpfee                  bump the tx fee
  chaintip                 return the height of the chain
  createmultisigsignature  create a p2sh multisig signature
  currentaddress           get the current bitcoin address
  dumpheaders              print the header database
  estimatefee              estimate the fee for a tx
  getconfirmations         get the number of confirmations for a tx
  getfeeperbyte            get the current bitcoin fee
  gettransaction           get a specific transaction
  haskey                   does key exist
  masterprivatekey         get the wallet's master private key
  masterpublickey          get the wallet's master public key
  multisign                combine multisig signatures
  newaddress               get a new bitcoin address
  peers                    get info about peers
  resyncblockchain         re-download the chain of headers
  spend                    send bitcoins
  start                    start the wallet
  stop                     stop the wallet
  sweepaddress             sweep all coins from an address
  transactions             get a list of transactions
  version                  print the version number

```

Finally a gRPC API is available on port 8234. The same interface is exposed via the API plus a streaming wallet notifier which fires when a new transaction (either incoming or outgoing) is recorded then again when it gains its first confirmation.