// Copyright (C) 2015-2016 The Lightning Network Developers
// Copyright (c) 2016-2017 The OpenBazaar Developers

package bitcoincash

import (
	"bytes"
	"errors"
	"sync"
	"time"

	"github.com/BubbaJoe/spvwallet-cash/wallet-interface"
	"github.com/gcash/bchd/blockchain"
	"github.com/gcash/bchd/chaincfg"
	"github.com/gcash/bchd/chaincfg/chainhash"
	"github.com/gcash/bchd/txscript"
	"github.com/gcash/bchd/wire"
	"github.com/gcash/bchutil"
	"github.com/gcash/bchutil/bloom"
)

type TxStore struct {
	adrs           []bchutil.Address
	watchedScripts [][]byte
	txids          map[string]int32
	addrMutex      *sync.Mutex
	txidsMutex     *sync.RWMutex
	cbMutex        *sync.Mutex
	showEveryTx    map[int]bool

	keyManager *KeyManager

	params *chaincfg.Params

	listeners []func(wallet.TransactionCallback)

	additionalFilters [][]byte

	wallet.Datastore
}

func NewTxStore(p *chaincfg.Params, db wallet.Datastore, keyManager *KeyManager, additionalFilters ...[]byte) (*TxStore, error) {
	txs := &TxStore{
		params:            p,
		keyManager:        keyManager,
		addrMutex:         new(sync.Mutex),
		cbMutex:           new(sync.Mutex),
		txidsMutex:        new(sync.RWMutex),
		txids:             make(map[string]int32),
		showEveryTx:       make(map[int]bool),
		Datastore:         db,
		additionalFilters: additionalFilters,
	}
	err := txs.PopulateAdrs()
	if err != nil {
		return nil, err
	}
	return txs, nil
}

// ... or I'm gonna fade away
func (ts *TxStore) GimmeFilter() (*bloom.Filter, error) {
	ts.PopulateAdrs()

	// get all utxos to add outpoints to filter
	allUtxos, err := ts.Utxos().GetAll()
	if err != nil {
		return nil, err
	}

	allStxos, err := ts.Stxos().GetAll()
	if err != nil {
		return nil, err
	}
	ts.addrMutex.Lock()
	elem := uint32(len(ts.adrs)+len(allUtxos)+len(allStxos)) + uint32(len(ts.watchedScripts))
	f := bloom.NewFilter(elem, 0, 1, wire.BloomUpdateAll)

	// note there could be false positives since we're just looking
	// for the 20 byte PKH without the opcodes.
	for _, a := range ts.adrs { // add 20-byte pubkeyhash
		f.Add(a.ScriptAddress())
	}
	ts.addrMutex.Unlock()
	for _, u := range allUtxos {
		f.AddOutPoint(&u.Op)
	}

	for _, s := range allStxos {
		f.AddOutPoint(&s.Utxo.Op)
	}
	for _, w := range ts.watchedScripts {
		_, addrs, _, err := txscript.ExtractPkScriptAddrs(w, ts.params)
		if err != nil {
			continue
		}
		f.Add(addrs[0].ScriptAddress())
	}
	for _, toAdd := range ts.additionalFilters {
		f.Add(toAdd)
	}
	return f, nil
}

// GetDoubleSpends takes a transaction and compares it with
// all transactions in the db.  It returns a slice of all txids in the db
// which are double spent by the received tx.
func (ts *TxStore) CheckDoubleSpends(argTx *wire.MsgTx) ([]*chainhash.Hash, error) {
	var dubs []*chainhash.Hash // slice of all double-spent txs
	argTxid := argTx.TxHash()
	txs, err := ts.Txns().GetAll(true)
	if err != nil {
		return dubs, err
	}
	for _, compTx := range txs {
		if compTx.Height < 0 {
			continue
		}
		r := bytes.NewReader(compTx.Bytes)
		msgTx := wire.NewMsgTx(1)
		msgTx.BchDecode(r, 1, wire.BaseEncoding)
		compTxid := msgTx.TxHash()
		for _, argIn := range argTx.TxIn {
			// iterate through inputs of compTx
			for _, compIn := range msgTx.TxIn {
				if outPointsEqual(argIn.PreviousOutPoint, compIn.PreviousOutPoint) && !compTxid.IsEqual(&argTxid) {
					// found double spend
					dubs = append(dubs, &compTxid)
					break // back to argIn loop
				}
			}
		}
	}
	return dubs, nil
}

// GetPendingInv returns an inv message containing all txs known to the
// db which are at height 0 (not known to be confirmed).
// This can be useful on startup or to rebroadcast unconfirmed txs.
func (ts *TxStore) GetPendingInv() (*wire.MsgInv, error) {
	// use a map (really a set) do avoid dupes
	txidMap := make(map[chainhash.Hash]struct{})

	utxos, err := ts.Utxos().GetAll() // get utxos from db
	if err != nil {
		return nil, err
	}
	stxos, err := ts.Stxos().GetAll() // get stxos from db
	if err != nil {
		return nil, err
	}

	// iterate through utxos, adding txids of anything with height 0
	for _, utxo := range utxos {
		if utxo.AtHeight == 0 {
			txidMap[utxo.Op.Hash] = struct{}{} // adds to map
		}
	}
	// do the same with stxos based on height at which spent
	for _, stxo := range stxos {
		if stxo.SpendHeight == 0 {
			txidMap[stxo.SpendTxid] = struct{}{}
		}
	}

	invMsg := wire.NewMsgInv()
	for txid := range txidMap {
		item := wire.NewInvVect(wire.InvTypeTx, &txid)
		err = invMsg.AddInvVect(item)
		if err != nil {
			return nil, err
		}
	}

	// return inv message with all txids (maybe none)
	return invMsg, nil
}

// PopulateAdrs just puts a bunch of adrs in ram; it doesn't touch the DB
func (ts *TxStore) PopulateAdrs() error {
	keys := ts.keyManager.GetKeys()
	ts.addrMutex.Lock()
	ts.adrs = []bchutil.Address{}
	for _, k := range keys {
		addr, err := k.Address(ts.params)
		if err != nil {
			continue
		}
		ts.adrs = append(ts.adrs, addr)
	}
	ts.addrMutex.Unlock()
	ts.watchedScripts, _ = ts.WatchedScripts().GetAll()
	txns, _ := ts.Txns().GetAll(true)
	ts.txidsMutex.Lock()
	for _, t := range txns {
		ts.txids[t.Txid] = t.Height
	}
	ts.txidsMutex.Unlock()
	return nil
}

// Ingest puts a tx into the DB atomically.  This can result in a
// gain, a loss, or no result.  Gain or loss in satoshis is returned.
func (ts *TxStore) Ingest(tx *wire.MsgTx, height int32, timestamp time.Time) (uint32, error) {
	var hits uint32
	var err error
	// Tx has been OK'd by SPV; check tx sanity
	utilTx := bchutil.NewTx(tx) // convert for validation
	// Checks basic stuff like there are inputs and ouputs
	err = blockchain.CheckTransactionSanity(utilTx, true, txscript.StandardVerifyFlags)
	if err != nil {
		return hits, err
	}

	// Check to see if we've already processed this tx. If so, return.

	ts.txidsMutex.RLock()
	sh, ok := ts.txids[tx.TxHash().String()]
	ts.txidsMutex.RUnlock()
	if ok && (sh > 0 || (sh == 0 && height == 0)) {
		return 0, nil
	}

	// Check to see if this is a double spend
	doubleSpends, err := ts.CheckDoubleSpends(tx)
	if err != nil {
		return hits, err
	}
	if len(doubleSpends) > 0 {
		// First seen rule
		if height == 0 {
			return 0, nil
		}
		// Mark any unconfirmed doubles as dead
		for _, double := range doubleSpends {
			ts.markAsDead(*double)
		}
	}

	// Generate PKscripts for all addresses
	ts.addrMutex.Lock()
	PKscripts := make([][]byte, len(ts.adrs))
	for i := range ts.adrs {
		// Iterate through all our addresses
		// TODO: This will need to test both segwit and legacy once segwit activates
		PKscripts[i], err = txscript.PayToAddrScript(ts.adrs[i])
		if err != nil {
			ts.addrMutex.Unlock()
			return hits, err
		}
	}
	ts.addrMutex.Unlock()

	// Iterate through all outputs of this tx, see if we gain
	cachedSha := tx.TxHash()
	cb := wallet.TransactionCallback{Txid: cachedSha.String(), Height: height}
	value := int64(0)
	matchesWatchOnly := false
	for i, txout := range tx.TxOut {
		// Ignore the error here because the sender could have used and exotic script
		// for his change and we don't want to fail in that case.
		addr, _ := scriptToAddress(txout.PkScript, ts.params)
		out := wallet.TransactionOutput{Address: addr, Value: txout.Value, Index: uint32(i)}
		for _, script := range PKscripts {
			if bytes.Equal(txout.PkScript, script) { // new utxo found
				scriptAddress, _ := ts.extractScriptAddress(txout.PkScript)
				ts.keyManager.MarkKeyAsUsed(scriptAddress)
				newop := wire.OutPoint{
					Hash:  cachedSha,
					Index: uint32(i),
				}
				newu := wallet.Utxo{
					AtHeight:     height,
					Value:        txout.Value,
					ScriptPubkey: txout.PkScript,
					Op:           newop,
					WatchOnly:    false,
				}
				value += newu.Value
				ts.Utxos().Put(newu)
				hits++
				break
			}
		}
		// Now check watched scripts
		for _, script := range ts.watchedScripts {
			if bytes.Equal(txout.PkScript, script) {
				newop := wire.OutPoint{
					Hash:  cachedSha,
					Index: uint32(i),
				}
				newu := wallet.Utxo{
					AtHeight:     height,
					Value:        txout.Value,
					ScriptPubkey: txout.PkScript,
					Op:           newop,
					WatchOnly:    true,
				}
				ts.Utxos().Put(newu)
				matchesWatchOnly = true
			}
		}
		for _, f := range ts.additionalFilters {
			if bytes.Contains(txout.PkScript, f) {
				matchesWatchOnly = true
				break
			}
		}
		cb.Outputs = append(cb.Outputs, out)
	}
	utxos, err := ts.Utxos().GetAll()
	if err != nil {
		return 0, err
	}
	for _, txin := range tx.TxIn {
		_bcutxo := false
		for i, u := range utxos {
			if outPointsEqual(txin.PreviousOutPoint, u.Op) {
				_bcutxo = true
				st := wallet.Stxo{
					Utxo:        u,
					SpendHeight: height,
					SpendTxid:   cachedSha,
				}
				ts.Stxos().Put(st)
				ts.Utxos().Delete(u)
				utxos = append(utxos[:i], utxos[i+1:]...)
				if !u.WatchOnly {
					value -= u.Value
					hits++
				} else {
					matchesWatchOnly = true
				}

				// Ignore the error here because the sender could have used and exotic script
				// for his change and we don't want to fail in that case.
				addr, _ := scriptToAddress(u.ScriptPubkey, ts.params)

				in := wallet.TransactionInput{
					OutpointHash:  u.Op.Hash.CloneBytes(),
					OutpointIndex: u.Op.Index,
					LinkedAddress: addr,
					Value:         u.Value,
				}
				cb.Inputs = append(cb.Inputs, in)
				break
			}
		}
		if !_bcutxo {
			in := wallet.TransactionInput{
				OutpointHash:  txin.PreviousOutPoint.Hash.CloneBytes(),
				OutpointIndex: txin.PreviousOutPoint.Index,
				LinkedAddress: nil,
				Value:         -1,
			}
			cb.Inputs = append(cb.Inputs, in)
		}
	}

	// Update height of any stxos
	if height > 0 {
		stxos, err := ts.Stxos().GetAll()
		if err != nil {
			return 0, err
		}
		for _, stxo := range stxos {
			if stxo.SpendTxid.IsEqual(&cachedSha) {
				stxo.SpendHeight = height
				ts.Stxos().Put(stxo)
				if !stxo.Utxo.WatchOnly {
					hits++
				} else {
					matchesWatchOnly = true
				}
				break
			}
		}
	}

	// If hits is nonzero it's a relevant tx and we should store it
	if hits > 0 || matchesWatchOnly {
		ts.txidsMutex.Lock()
		txn, err := ts.Txns().Get(tx.TxHash())
		shouldCallback := false
		if err != nil {
			cb.Value = value
			txn.Timestamp = timestamp
			shouldCallback = true
			var buf bytes.Buffer
			tx.BchEncode(&buf, 1, wire.BaseEncoding)
			ts.Txns().Put(buf.Bytes(), tx.TxHash().String(), int(value), int(height), txn.Timestamp, hits == 0)
			ts.txids[tx.TxHash().String()] = height
		}
		// Let's check the height before committing so we don't allow rogue peers to send us a lose
		// tx that resets our height to zero.
		if txn.Height <= 0 {
			ts.Txns().UpdateHeight(tx.TxHash(), int(height), timestamp)
			ts.txids[tx.TxHash().String()] = height
			if height > 0 {
				cb.Value = txn.Value
				shouldCallback = true
			}
		}
		cb.BlockTime = timestamp
		ts.txidsMutex.Unlock()
		if shouldCallback {
			ts.cbMutex.Lock()
			go func() {
				// Callback on listeners
				for _, listener := range ts.listeners {
					if listener != nil {
						listener(cb)
					}
				}
			}()
			ts.cbMutex.Unlock()
		}
		ts.PopulateAdrs()
		hits++
	} else {
		cb.BlockTime = timestamp
		go func() {
			ts.cbMutex.Lock()
			for i, listener := range ts.listeners {
				if shouldShow, ok := ts.showEveryTx[i]; ok && shouldShow && (listener != nil) {
					listener(cb)
				}
			}
			ts.cbMutex.Unlock()
		}()
	}

	return hits, err
}

func (ts *TxStore) markAsDead(txid chainhash.Hash) error {
	stxos, err := ts.Stxos().GetAll()
	if err != nil {
		return err
	}
	markStxoAsDead := func(s wallet.Stxo) error {
		err := ts.Stxos().Delete(s)
		if err != nil {
			return err
		}
		err = ts.Txns().UpdateHeight(s.SpendTxid, -1, time.Now())
		if err != nil {
			return err
		}
		return nil
	}
	for _, s := range stxos {
		// If an stxo is marked dead, move it back into the utxo table
		if txid.IsEqual(&s.SpendTxid) {
			if err := markStxoAsDead(s); err != nil {
				return err
			}
			if err := ts.Utxos().Put(s.Utxo); err != nil {
				return err
			}
		}
		// If a dependency of the spend is dead then mark the spend as dead
		if txid.IsEqual(&s.Utxo.Op.Hash) {
			if err := markStxoAsDead(s); err != nil {
				return err
			}
			if err := ts.markAsDead(s.SpendTxid); err != nil {
				return err
			}
		}
	}
	utxos, err := ts.Utxos().GetAll()
	if err != nil {
		return err
	}
	// Dead utxos should just be deleted
	for _, u := range utxos {
		if txid.IsEqual(&u.Op.Hash) {
			err := ts.Utxos().Delete(u)
			if err != nil {
				return err
			}
		}
	}
	ts.Txns().UpdateHeight(txid, -1, time.Now())
	return nil
}

func (ts *TxStore) processReorg(lastGoodHeight uint32) error {
	txns, err := ts.Txns().GetAll(true)
	if err != nil {
		return err
	}
	for i := len(txns) - 1; i >= 0; i-- {
		if txns[i].Height > int32(lastGoodHeight) {
			txid, err := chainhash.NewHashFromStr(txns[i].Txid)
			if err != nil {
				log.Error(err)
				continue
			}
			err = ts.markAsDead(*txid)
			if err != nil {
				log.Error(err)
				continue
			}
		}
	}
	return nil
}

func (ts *TxStore) extractScriptAddress(script []byte) ([]byte, error) {
	_, addrs, _, err := txscript.ExtractPkScriptAddrs(script, ts.params)
	if err != nil {
		return nil, err
	}
	if len(addrs) == 0 {
		return nil, errors.New("unknown script")
	}
	return addrs[0].ScriptAddress(), nil
}

func outPointsEqual(a, b wire.OutPoint) bool {
	if !a.Hash.IsEqual(&b.Hash) {
		return false
	}
	return a.Index == b.Index
}
