package bitcoincash

import (
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"time"

	"github.com/BubbaJoe/spvwallet-cash/exchangerates"
	"github.com/BubbaJoe/spvwallet-cash/wallet-interface"
	db "github.com/bubbajoe/spvwallet-cash/db"
	"github.com/gcash/bchd/bchec"
	"github.com/gcash/bchd/chaincfg"
	"github.com/gcash/bchd/chaincfg/chainhash"
	"github.com/gcash/bchd/peer"
	"github.com/gcash/bchd/txscript"
	"github.com/gcash/bchutil"
	hd "github.com/gcash/bchutil/hdkeychain"
	"github.com/gcash/bchwallet/wallet/txrules"
	"github.com/op/go-logging"
	b39 "github.com/tyler-smith/go-bip39"
)

type SPVWallet struct {
	params *chaincfg.Params

	masterPrivateKey *hd.ExtendedKey
	masterPublicKey  *hd.ExtendedKey

	mnemonic string

	feeProvider *FeeProvider

	repoPath string

	blockchain  *Blockchain
	txstore     *TxStore
	peerManager *PeerManager
	keyManager  *KeyManager
	wireService *WireService

	fPositives    chan *peer.Peer
	stopChan      chan int
	fpAccumulator map[int32]int32
	mutex         *sync.RWMutex

	creationDate time.Time

	running bool

	config *PeerManagerConfig

	exchangeRates wallet.ExchangeRates
}

var log = logging.MustGetLogger("bitcoin")

const WALLET_VERSION = "0.4.0"

func NewSPVWallet(config *Config) (*SPVWallet, error) {
	log.SetBackend(logging.AddModuleLevel(config.Logger))

	if config.Mnemonic == "" {
		ent, err := b39.NewEntropy(128)
		if err != nil {
			return nil, err
		}
		mnemonic, err := b39.NewMnemonic(ent)
		if err != nil {
			return nil, err
		}
		config.Mnemonic = mnemonic
		config.CreationDate = time.Now()
	}
	seed := b39.NewSeed(config.Mnemonic, "")

	mPrivKey, err := hd.NewMaster(seed, config.Params)
	if err != nil {
		return nil, err
	}
	mPubKey, err := mPrivKey.Neuter()
	if err != nil {
		return nil, err
	}

	err = saveConfig(config)
	if err != nil {
		return nil, err
	}

	w := &SPVWallet{
		repoPath:         config.RepoPath,
		masterPrivateKey: mPrivKey,
		masterPublicKey:  mPubKey,
		mnemonic:         config.Mnemonic,
		params:           config.Params,
		creationDate:     config.CreationDate,
		feeProvider:      NewFeeProvider(3, 2, 1, 1, nil),
		fPositives:       make(chan *peer.Peer),
		stopChan:         make(chan int),
		fpAccumulator:    make(map[int32]int32),
		mutex:            new(sync.RWMutex),
	}

	er := exchangerates.NewBitcoinCashPriceFetcher(config.Proxy)
	w.exchangeRates = er
	if !config.DisableExchangeRates {
		go er.Run()
		w.feeProvider.exchangeRates = er
	}

	w.keyManager, err = NewKeyManager(config.DB.Keys(), w.params, w.masterPrivateKey)

	w.txstore, err = NewTxStore(w.params, config.DB, w.keyManager, config.AdditionalFilters...)
	if err != nil {
		return nil, err
	}

	w.blockchain, err = NewBlockchain(w.repoPath, w.creationDate, w.params)
	if err != nil {
		return nil, err
	}

	minSync := 5
	if config.TrustedPeer != nil {
		minSync = 1
	}
	wireConfig := &WireServiceConfig{
		txStore:            w.txstore,
		chain:              w.blockchain,
		walletCreationDate: w.creationDate,
		minPeersForSync:    minSync,
		params:             w.params,
	}

	ws := NewWireService(wireConfig)
	w.wireService = ws

	getNewestBlock := func() (*chainhash.Hash, int32, error) {
		sh, err := w.blockchain.BestBlock()
		if err != nil {
			return nil, 0, err
		}
		h := sh.header.BlockHash()
		return &h, int32(sh.height), nil
	}

	w.config = &PeerManagerConfig{
		UserAgentName:    config.UserAgent,
		UserAgentVersion: WALLET_VERSION,
		Params:           w.params,
		AddressCacheDir:  config.RepoPath,
		Proxy:            config.Proxy,
		GetNewestBlock:   getNewestBlock,
		MsgChan:          ws.MsgChan(),
	}

	if config.TrustedPeer != nil {
		w.config.TrustedPeer = config.TrustedPeer
	}

	w.peerManager, err = NewPeerManager(w.config)
	if err != nil {
		return nil, err
	}

	return w, nil
}

func saveConfig(config *Config) error {
	ds, err := db.Create(config.RepoPath)
	if err != nil {
		return err
	}
	mnem, err := ds.GetMnemonic()
	if err != nil {
		ds.SetConfigKV("archived_mnemonic_"+strconv.Itoa(rand.Int()), mnem)
	}
	err = ds.SetMnemonic(config.Mnemonic)
	if err != nil {
		return err
	}
	ds.SetCreationDate(config.CreationDate)
	return nil
}

func (w *SPVWallet) Start() {
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	go func() {
		for range c {
			fmt.Println("\n\n\nspvwallet-cash shutting down...")
			w.Close()
			os.Exit(1)
		}
	}()
	w.running = true
	go w.wireService.Start()
	go w.peerManager.Start()
}

//////////////////////////////////////////////////////////////////////////////////////////////////////////////////
//
// API
//
//////////////

func (w *SPVWallet) CurrencyCode() string {
	if w.params.Name == chaincfg.MainNetParams.Name {
		return "bch"
	}
	return "tbch"
}

func (w *SPVWallet) CreationDate() time.Time {
	return w.creationDate
}

func (w *SPVWallet) IsDust(amount int64) bool {
	return txrules.IsDustAmount(bchutil.Amount(amount), 25, txrules.DefaultRelayFeePerKb)
}

func (w *SPVWallet) MasterPrivateKey() *hd.ExtendedKey {
	return w.masterPrivateKey
}

func (w *SPVWallet) MasterPublicKey() *hd.ExtendedKey {
	return w.masterPublicKey
}

func (w *SPVWallet) ChildKey(keyBytes []byte, chaincode []byte, isPrivateKey bool) (*hd.ExtendedKey, error) {
	parentFP := []byte{0x00, 0x00, 0x00, 0x00}
	var id []byte
	if isPrivateKey {
		id = w.params.HDPrivateKeyID[:]
	} else {
		id = w.params.HDPublicKeyID[:]
	}
	hdKey := hd.NewExtendedKey(
		id, keyBytes, chaincode, parentFP,
		0, 0, isPrivateKey)

	return hdKey.Child(0)
}

func (w *SPVWallet) Mnemonic() string {
	return w.mnemonic
}

func (w *SPVWallet) ConnectedPeers() []*peer.Peer {
	return w.peerManager.ConnectedPeers()
}

func (w *SPVWallet) CurrentAddress(purpose wallet.KeyPurpose) bchutil.Address {
	key, _ := w.keyManager.GetCurrentKey(purpose)
	addr, _ := key.Address(w.params)
	cashaddr, _ := bchutil.NewAddressPubKeyHash(addr.ScriptAddress(), w.params)
	return bchutil.Address(cashaddr)
}

func (w *SPVWallet) NewAddress(purpose wallet.KeyPurpose) bchutil.Address {
	i, _ := w.txstore.Keys().GetUnused(purpose)
	key, _ := w.keyManager.generateChildKey(purpose, uint32(i[1]))
	addr, _ := key.Address(w.params)
	w.txstore.Keys().MarkKeyAsUsed(addr.ScriptAddress())
	w.txstore.PopulateAdrs()
	cashaddr, _ := bchutil.NewAddressPubKeyHash(addr.ScriptAddress(), w.params)
	return bchutil.Address(cashaddr)
}

func (w *SPVWallet) DecodeAddress(addr string) (bchutil.Address, error) {
	// Legacy and Cash Address
	decoded, err := bchutil.DecodeAddress(addr, w.params)
	if err == nil {
		return decoded, nil
	}

	return nil, errors.New("Unrecognized address format")
}

func (w *SPVWallet) ScriptToAddress(script []byte) (bchutil.Address, error) {
	return scriptToAddress(script, w.params)
}

func scriptToAddress(script []byte, params *chaincfg.Params) (bchutil.Address, error) {
	_, addr, i, err := txscript.ExtractPkScriptAddrs(script, params)
	if err != nil {
		return nil, err
	}
	if i != 1 {
		return nil, errors.New("Error: " + string(i) + " addresses were found in this script")
	}
	return addr[0], nil
}

func scriptToAddresses(script []byte, params *chaincfg.Params) ([]bchutil.Address, txscript.ScriptClass, error) {
	sc, addr, _, err := txscript.ExtractPkScriptAddrs(script, params)
	if err != nil {
		return nil, 0, err
	}
	return addr, sc, nil
}

func (w *SPVWallet) AddressToScript(addr bchutil.Address) ([]byte, error) {
	return txscript.PayToAddrScript(addr)
}

func (w *SPVWallet) HasKey(addr bchutil.Address) bool {
	_, err := w.keyManager.GetKeyForScript(addr.ScriptAddress())
	if err != nil {
		return false
	}
	return true
}

func (w *SPVWallet) GetKey(addr bchutil.Address) (*bchec.PrivateKey, error) {
	key, err := w.keyManager.GetKeyForScript(addr.ScriptAddress())
	if err != nil {
		return nil, err
	}
	return key.ECPrivKey()
}

func (w *SPVWallet) ListAddresses() []bchutil.Address {
	keys := w.keyManager.GetKeys()
	addrs := []bchutil.Address{}
	for _, k := range keys {
		addr, err := k.Address(w.params)
		if err != nil {
			continue
		}
		cashaddr, err := bchutil.NewAddressPubKeyHash(addr.ScriptAddress(), w.params)
		if err != nil {
			continue
		}
		addrs = append(addrs, cashaddr)
	}
	return addrs
}

func (w *SPVWallet) ListKeys() []bchec.PrivateKey {
	keys := w.keyManager.GetKeys()
	list := []bchec.PrivateKey{}
	for _, k := range keys {
		priv, err := k.ECPrivKey()
		if err != nil {
			continue
		}
		list = append(list, *priv)
	}
	return list
}

func (w *SPVWallet) ImportKey(privKey *bchec.PrivateKey, compress bool) error {
	pub := privKey.PubKey()
	var pubKeyBytes []byte
	if compress {
		pubKeyBytes = pub.SerializeCompressed()
	} else {
		pubKeyBytes = pub.SerializeUncompressed()
	}
	pkHash := bchutil.Hash160(pubKeyBytes)
	addr, err := bchutil.NewAddressPubKeyHash(pkHash, w.params)
	if err != nil {
		return err
	}
	return w.keyManager.datastore.ImportKey(addr.ScriptAddress(), privKey)
}

func (w *SPVWallet) Balance() (confirmed, unconfirmed int64) {
	utxos, _ := w.txstore.Utxos().GetAll()
	stxos, _ := w.txstore.Stxos().GetAll()
	for _, utxo := range utxos {
		if !utxo.WatchOnly {
			confs, _, err := w.GetConfirmations(utxo.Op.Hash)
			if err != nil {
				continue
			}
			if confs > 0 {
				confirmed += utxo.Value
			} else {
				// TODO: Need to check if the utxo is spent
				if w.checkIfStxoIsConfirmed(utxo, stxos) {
					confirmed += utxo.Value
				} else {
					unconfirmed += utxo.Value
				}
			}

		}
	}
	return confirmed, unconfirmed
}

func (w *SPVWallet) Transactions() ([]wallet.Txn, error) {
	height, _ := w.ChainTip()
	txns, err := w.txstore.Txns().GetAll(false)
	if err != nil {
		return txns, err
	}
	for i, tx := range txns {
		var confirmations int32
		var status wallet.StatusCode
		confs := int32(height) - tx.Height + 1
		if tx.Height <= 0 {
			confs = tx.Height
		}
		switch {
		case confs < 0:
			status = wallet.StatusDead
		case confs == 0 && time.Since(tx.Timestamp) <= time.Hour*6:
			status = wallet.StatusUnconfirmed
		case confs == 0 && time.Since(tx.Timestamp) > time.Hour*6:
			status = wallet.StatusStuck
		case confs > 0 && confs < 6:
			status = wallet.StatusPending
			confirmations = confs
		case confs > 5:
			status = wallet.StatusConfirmed
			confirmations = confs
		}
		tx.Confirmations = int64(confirmations)
		tx.Status = status
		txns[i] = tx
	}
	return txns, nil
}

func (w *SPVWallet) GetTransaction(txid chainhash.Hash) (wallet.Txn, error) {
	txn, err := w.txstore.Txns().Get(txid)
	return txn, err
}

func (w *SPVWallet) GetConfirmations(txid chainhash.Hash) (uint32, uint32, error) {
	txn, err := w.txstore.Txns().Get(txid)
	if err != nil {
		return 0, 0, err
	}
	if txn.Height == 0 {
		return 0, 0, nil
	}
	chainTip, _ := w.ChainTip()
	return chainTip - uint32(txn.Height) + 1, uint32(txn.Height), nil
}

func (w *SPVWallet) checkIfStxoIsConfirmed(utxo wallet.Utxo, stxos []wallet.Stxo) bool {
	for _, stxo := range stxos {
		if !stxo.Utxo.WatchOnly {
			if stxo.SpendTxid.IsEqual(&utxo.Op.Hash) {
				if stxo.SpendHeight > 0 {
					return true
				} else {
					return w.checkIfStxoIsConfirmed(stxo.Utxo, stxos)
				}
			} else if stxo.Utxo.IsEqual(&utxo) {
				if stxo.Utxo.AtHeight > 0 {
					return true
				} else {
					return false
				}
			}
		}
	}
	return false
}

func (w *SPVWallet) Params() *chaincfg.Params {
	return w.params
}

func (w *SPVWallet) AddTransactionListener(everyTx bool, callback func(wallet.TransactionCallback)) int {
	w.txstore.showEveryTx[len(w.txstore.listeners)] = everyTx
	w.txstore.listeners = append(w.txstore.listeners, callback)
	return len(w.txstore.listeners) - 1
}

func (w *SPVWallet) RemoveTransactionListener(cbId int) error {
	if _, ok := w.txstore.showEveryTx[cbId]; !ok {
		return errors.New("invalid transaction listener id")
	}
	w.txstore.listeners[cbId] = nil
	delete(w.txstore.showEveryTx, cbId)
	return nil
}

func (w *SPVWallet) AddBlockListener(tipOnly bool, callback func(wallet.BlockCallback)) int {
	id := len(w.wireService.listeners)
	w.wireService.showTipOnly[id] = tipOnly
	w.wireService.listeners = append(w.wireService.listeners, callback)
	return id
}

func (w *SPVWallet) RemoveBlockListener(cbId int) error {
	if ok := w.wireService.showTipOnly[cbId]; !ok {
		return errors.New("invalid transaction listener id")
	}
	w.wireService.listeners[cbId] = nil
	delete(w.wireService.showTipOnly, cbId)
	return nil
}

func (w *SPVWallet) ChainTip() (uint32, chainhash.Hash) {
	var ch chainhash.Hash
	sh, err := w.blockchain.db.GetBestHeader()
	if err != nil {
		return 0, ch
	}
	return sh.height, sh.header.BlockHash()
}

func (w *SPVWallet) AddWatchedAddress(addr bchutil.Address) error {
	script, err := w.AddressToScript(addr)
	if err != nil {
		return err
	}
	err = w.txstore.WatchedScripts().Put(script)
	w.txstore.PopulateAdrs()

	w.wireService.MsgChan() <- updateFiltersMsg{}
	return err
}

func (w *SPVWallet) RemoveWatchedAddress(addr bchutil.Address) error {
	script, err := w.AddressToScript(addr)
	if err != nil {
		return err
	}
	err = w.txstore.WatchedScripts().Delete(script)
	w.txstore.PopulateAdrs()

	w.wireService.MsgChan() <- updateFiltersMsg{}
	return err
}

func (w *SPVWallet) DumpHeaders(writer io.Writer) {
	w.blockchain.db.Print(writer)
}

func (w *SPVWallet) ExchangeRates() wallet.ExchangeRates {
	return w.exchangeRates
}

func (w *SPVWallet) Close() {
	if w.running {
		log.Info("Disconnecting from peers and shutting down")
		w.peerManager.Stop()
		w.blockchain.Close()
		w.wireService.Stop()
		w.running = false
	}
}

func (w *SPVWallet) ReSyncBlockchain(fromDate time.Time) {
	w.blockchain.Rollback(fromDate)
	w.txstore.PopulateAdrs()
	w.wireService.Resync()
}

func (w *SPVWallet) ResyncBlockchainHeight(height int32) {
	if height < 0 {
		height += int32(w.blockchain.checkpoint.Height)
	}
	w.blockchain.RollbackToHeight(uint32(height))
	w.txstore.PopulateAdrs()
	w.wireService.Resync()
}
