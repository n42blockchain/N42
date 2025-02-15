// Copyright 2022 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package miner

import (
	"context"
	"errors"
	"fmt"
	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/core"
	"github.com/n42blockchain/N42/internal/api"
	"github.com/n42blockchain/N42/internal/consensus/misc"
	"github.com/n42blockchain/N42/internal/metrics/prometheus"
	"sort"
	"sync"

	"github.com/n42blockchain/N42/modules/rawdb"

	"github.com/n42blockchain/N42/internal"
	vm2 "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/modules/state"

	"sync/atomic"
	"time"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"

	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/log"
	event "github.com/n42blockchain/N42/modules/event/v2"
	"github.com/n42blockchain/N42/params"

	mapset "github.com/deckarep/golang-set"
	"golang.org/x/sync/errgroup"
)

var (
	blockSignGauge = prometheus.GetOrCreateCounter("block_sign_counter", true)
)

type task struct {
	receipts  []*block.Receipt
	state     *state.IntraBlockState
	block     block.IBlock
	createdAt time.Time
	nopay     map[types.Address]*uint256.Int
}

type newWorkReq struct {
	interrupt *atomic.Int32
	noempty   bool
	timestamp int64
}

type generateParams struct {
	timestamp  uint64        // The timstamp for sealing task
	parentHash types.Hash    // Parent block hash, empty means the latest chain head
	coinbase   types.Address // The fee recipient address for including transaction
	random     types.Hash    // The randomness generated by beacon chain, empty before the merge
	noTxs      bool          // Flag whether an empty block without any transaction is expected
}

type environment struct {
	//signer types.Signer

	//state     *state.IntraBlockState
	ancestors mapset.Set      // ancestor set (used for checking uncle parent validity)
	family    mapset.Set      // family set (used for checking uncle invalidity)
	tcount    int             // tx count in cycle
	gasPool   *common.GasPool // available gas used to pack transactions
	coinbase  types.Address

	header   *block.Header
	txs      []*transaction.Transaction
	receipts []*block.Receipt
}

func (env *environment) copy() *environment {
	cpy := &environment{
		ancestors: env.ancestors.Clone(),
		family:    env.family.Clone(),
		tcount:    env.tcount,
		coinbase:  env.coinbase,
		header:    block.CopyHeader(env.header),
		receipts:  env.receipts,
	}
	if env.gasPool != nil {
		gasPool := *env.gasPool
		cpy.gasPool = &gasPool
	}

	cpy.txs = make([]*transaction.Transaction, len(env.txs))
	copy(cpy.txs, env.txs)
	return cpy
}

const (
	commitInterruptNone int32 = iota
	commitInterruptNewHead
	commitInterruptResubmit
	commitInterruptTimeout
)

const (
	minPeriodInterval      = 1 * time.Second // 1s
	staleThreshold         = 7
	resubmitAdjustChanSize = 10

	// maxRecommitInterval is the maximum time interval to recreate the sealing block with
	// any newly arrived transactions.
	maxRecommitInterval = 12 * time.Second

	intervalAdjustRatio = 0.1

	intervalAdjustBias = 200 * 1000.0 * 1000.0
)

var (
	errBlockInterruptedByNewHead  = errors.New("new head arrived while building block")
	errBlockInterruptedByRecommit = errors.New("recommit interrupt while building block")
	errBlockInterruptedByTimeout  = errors.New("timeout while building block")
)

// intervalAdjust represents a resubmitting interval adjustment.
type intervalAdjust struct {
	ratio float64
	inc   bool
}

type worker struct {
	minerConf conf.MinerConfig
	engine    consensus.Engine
	chain     common.IBlockChain
	txsPool   common.ITxsPool

	coinbase    types.Address
	chainConfig *params.ChainConfig

	isLocalBlock func(header *block.Header) bool
	pendingTasks map[types.Hash]*task

	wg sync.WaitGroup
	mu sync.RWMutex

	startCh   chan struct{}
	newWorkCh chan *newWorkReq
	resultCh  chan block.IBlock
	taskCh    chan *task

	resubmitAdjustCh chan *intervalAdjust

	running int32
	newTxs  int32

	group  *errgroup.Group
	ctx    context.Context
	cancel context.CancelFunc
	//current     *environment
	newTaskHook func(*task)

	snapshotMu       sync.RWMutex // The lock used to protect the snapshots below
	snapshotBlock    block.IBlock
	snapshotReceipts block.Receipts
}

func newWorker(ctx context.Context, group *errgroup.Group, chainConfig *params.ChainConfig, engine consensus.Engine, bc common.IBlockChain, txsPool common.ITxsPool, isLocalBlock func(header *block.Header) bool, init bool, minerConf conf.MinerConfig) *worker {
	c, cancel := context.WithCancel(ctx)
	worker := &worker{
		engine:           engine,
		chain:            bc,
		txsPool:          txsPool,
		chainConfig:      chainConfig,
		mu:               sync.RWMutex{},
		startCh:          make(chan struct{}, 1),
		group:            group,
		isLocalBlock:     isLocalBlock,
		ctx:              c,
		cancel:           cancel,
		taskCh:           make(chan *task),
		newWorkCh:        make(chan *newWorkReq),
		resultCh:         make(chan block.IBlock),
		pendingTasks:     make(map[types.Hash]*task),
		minerConf:        minerConf,
		resubmitAdjustCh: make(chan *intervalAdjust, resubmitAdjustChanSize),
	}
	recommit := worker.minerConf.Recommit
	if recommit < minPeriodInterval {
		recommit = minPeriodInterval
	}

	// machine verify
	group.Go(func() error {
		return api.MachineVerify(ctx)
	})

	group.Go(func() error {
		return worker.workLoop(recommit)
	})

	group.Go(func() error {
		return worker.runLoop()
	})

	group.Go(func() error {
		return worker.taskLoop()
	})

	group.Go(func() error {
		return worker.resultLoop()
	})

	if init {
		worker.startCh <- struct{}{}
	}

	return worker
}

func (w *worker) start() {
	atomic.StoreInt32(&w.running, 1)
	w.startCh <- struct{}{}
}

func (w *worker) stop() {
	atomic.StoreInt32(&w.running, 0)
}

func (w *worker) close() {

}

func (w *worker) isRunning() bool {
	return atomic.LoadInt32(&w.running) == 1
}
func (w *worker) setCoinbase(addr types.Address) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.coinbase = addr
}

func (w *worker) runLoop() error {
	defer w.cancel()
	defer w.stop()
	for {
		select {
		case <-w.ctx.Done():
			return w.ctx.Err()
		case req := <-w.newWorkCh:
			err := w.commitWork(req.interrupt, req.noempty, req.timestamp)
			if err != nil {
				log.Error("runLoop err:", err.Error())
				//w.startCh <- struct{}{}
			}
		}
	}
}

func (w *worker) resultLoop() error {
	defer w.cancel()
	defer w.stop()

	for {
		select {
		case <-w.ctx.Done():
			return w.ctx.Err()
		case blk := <-w.resultCh:
			if blk == nil {
				continue
			}

			// Short circuit when receiving duplicate result caused by resubmitting.
			if w.chain.HasBlock(blk.Hash(), blk.Number64().Uint64()) {
				continue
			}

			var (
				sealhash = w.engine.SealHash(blk.Header())
				hash     = blk.Hash()
			)
			w.mu.RLock()
			task, exist := w.pendingTasks[sealhash]
			w.mu.RUnlock()
			if !exist {
				log.Error("Block found but no relative pending task", "number", blk.Number64().Uint64(), "sealhash", sealhash, "hash", hash)
				//w.startCh <- struct{}{}
				continue
			}

			// Different block could share same sealhash, deep copy here to prevent write-write conflict.
			var (
				receipts = make([]*block.Receipt, len(task.receipts))
				logs     []*block.Log
			)
			for i, taskReceipt := range task.receipts {
				receipt := new(block.Receipt)
				receipts[i] = receipt
				*receipt = *taskReceipt

				// add block location fields
				receipt.BlockHash = hash
				receipt.BlockNumber = blk.Number64()
				receipt.TransactionIndex = uint(i)

				// Update the block hash in all logs since it is now available and not when the
				// receipt/log of individual transactions were created.
				receipt.Logs = make([]*block.Log, len(taskReceipt.Logs))
				for i, taskLog := range taskReceipt.Logs {
					log := new(block.Log)
					receipt.Logs[i] = log
					*log = *taskLog
					log.BlockHash = hash
				}
				logs = append(logs, receipt.Logs...)
			}

			// Commit block and state to database.
			err := w.chain.WriteBlockWithState(blk, receipts, task.state, task.nopay)
			if err != nil {
				log.Error("Failed writing block to chain", "err", err)
				continue
			}
			blockSignGauge.Set(uint64(len(blk.Body().Verifier())))

			if len(logs) > 0 {
				event.GlobalEvent.Send(common.NewLogsEvent{Logs: logs})
			}

			log.Info("🔨 Successfully sealed new block",
				"sealhash", sealhash,
				"hash", hash,
				"number", blk.Number64().Uint64(),
				"used gas", blk.GasUsed(),
				"diff", blk.Difficulty().Uint64(),
				"headerTime", time.Unix(int64(blk.Time()), 0).Format(time.RFC3339),
				"verifierCount", len(blk.Body().Verifier()),
				"rewardCount", len(blk.Body().Reward()),
				"elapsed", common.PrettyDuration(time.Since(task.createdAt)),
				"txs", len(blk.Transactions()))

			if err = w.chain.SealedBlock(blk); err != nil {
				log.Error("Failed Broadcast block to p2p network", "err", err)
				continue
			}
			event.GlobalEvent.Send(common.ChainHighestBlock{Block: *blk.(*block.Block), Inserted: true})
		}
	}
}

func (w *worker) taskLoop() error {
	defer w.cancel()
	defer w.stop()

	var (
		stopCh chan struct{}
		prev   types.Hash
	)

	interrupt := func() {
		if stopCh != nil {
			close(stopCh)
			stopCh = nil
		}
	}

	for {
		select {
		case <-w.ctx.Done():
			return w.ctx.Err()
		case task := <-w.taskCh:

			if w.newTaskHook != nil {
				w.newTaskHook(task)
			}

			sealHash := w.engine.SealHash(task.block.Header())
			hash := task.block.Hash()
			stateRoot := task.block.StateRoot()
			if sealHash == prev {
				continue
			}
			interrupt()
			stopCh, prev = make(chan struct{}), sealHash
			w.mu.Lock()
			w.pendingTasks[sealHash] = task
			w.mu.Unlock()

			if err := w.engine.Seal(w.chain, task.block, w.resultCh, stopCh); err != nil {
				w.mu.Lock()
				delete(w.pendingTasks, sealHash)
				w.mu.Unlock()
				log.Warn("delete task", "sealHash", sealHash, "hash", hash, "stateRoot", stateRoot, "err", err)
				if errors.Is(err, consensus.ErrNotEnoughSign) {
					time.Sleep(1 * time.Second)
					w.startCh <- struct{}{}
				}
			} else {
				log.Debug("send task", "sealHash", sealHash, "hash", hash, "stateRoot", stateRoot)
			}
		}
	}
}

func (w *worker) commitWork(interrupt *atomic.Int32, noempty bool, timestamp int64) error {
	start := time.Now()
	if w.isRunning() {
		if w.coinbase == (types.Address{}) {
			return fmt.Errorf("coinbase is empty")
		}
	}

	current, err := w.prepareWork(&generateParams{timestamp: uint64(timestamp), coinbase: w.coinbase})
	if err != nil {
		log.Error("cannot prepare work", "err", err)
		return err
	}

	tx, err := w.chain.DB().BeginRo(w.ctx)
	if nil != err {
		log.Error("work.commitWork failed", err)
		return err
	}
	defer tx.Rollback()

	stateReader := state.NewPlainStateReader(tx)
	stateWriter := state.NewNoopWriter()
	ibs := state.New(stateReader)
	// generate state for mobile verify
	ibs.BeginWriteSnapshot()
	ibs.BeginWriteCodes()
	headers := make([]*block.Header, 0)
	getHeader := func(hash types.Hash, number uint64) *block.Header {
		h := rawdb.ReadHeader(tx, hash, number)
		if nil != h {
			headers = append(headers, h)
		}
		return h
	}

	err = w.fillTransactions(interrupt, current, ibs, getHeader)
	switch {
	case err == nil:
		w.resubmitAdjustCh <- &intervalAdjust{inc: false}
	case errors.Is(err, errBlockInterruptedByRecommit):
		gaslimit := current.header.GasLimit
		ratio := float64(gaslimit-current.gasPool.Gas()) / float64(gaslimit)
		if ratio < 0.1 {
			ratio = 0.1
		}
		w.resubmitAdjustCh <- &intervalAdjust{
			ratio: ratio,
			inc:   true,
		}
	}

	//var rewards []*block.Reward
	//if w.chainConfig.IsBeijing(current.header.Number.Uint64()) {
	//	rewards, err = w.engine.Rewards(tx, block.CopyHeader(current.header), ibs, false)
	//	if err != nil {
	//		return err
	//	}
	//}

	if err = w.commit(current, stateWriter, ibs, start, headers); nil != err {
		log.Errorf("w.commit failed, error %v\n", err)
		return err
	}

	return nil
}

// recalcRecommit recalculates the resubmitting interval upon feedback.
func recalcRecommit(minRecommit, prev time.Duration, target float64, inc bool) time.Duration {
	var (
		prevF = float64(prev.Nanoseconds())
		next  float64
	)
	if inc {
		next = prevF*(1-intervalAdjustRatio) + intervalAdjustRatio*(target+intervalAdjustBias)
		max := float64(maxRecommitInterval.Nanoseconds())
		if next > max {
			next = max
		}
	} else {
		next = prevF*(1-intervalAdjustRatio) + intervalAdjustRatio*(target-intervalAdjustBias)
		min := float64(minRecommit.Nanoseconds())
		if next < min {
			next = min
		}
	}
	return time.Duration(int64(next))
}

func (w *worker) workLoop(recommit time.Duration) error {
	defer w.cancel()
	defer w.stop()
	var (
		interrupt   *atomic.Int32
		minRecommit = recommit // minimal resubmit interval specified by user.
		timestamp   int64      // timestamp for each round of sealing.
	)

	newBlockCh := make(chan common.ChainHighestBlock)
	defer close(newBlockCh)

	newBlockSub := event.GlobalEvent.Subscribe(newBlockCh)
	defer newBlockSub.Unsubscribe()

	timer := time.NewTimer(0)
	defer timer.Stop()
	<-timer.C // discard the initial tick

	commit := func(noempty bool, s int32) {
		if interrupt != nil {
			interrupt.Store(s)
		}
		interrupt = new(atomic.Int32)
		select {
		case w.newWorkCh <- &newWorkReq{interrupt: interrupt, noempty: noempty, timestamp: timestamp}:
		case <-w.ctx.Done():
			return
		}
		timer.Reset(recommit)
		//atomic.StoreInt32(&w.newTxs, 0)
	}

	clearPending := func(number *uint256.Int) {
		w.mu.Lock()
		for h, t := range w.pendingTasks {
			if number.Cmp(uint256.NewInt(0).Add(t.block.Number64(), uint256.NewInt(staleThreshold))) < 1 {
				delete(w.pendingTasks, h)
			}
		}
		w.mu.Unlock()
	}

	for {
		select {
		case <-w.ctx.Done():
			return w.ctx.Err()
		case <-w.startCh:
			clearPending(w.chain.CurrentBlock().Number64())
			timestamp = time.Now().Unix()
			commit(false, commitInterruptNewHead)

		case blockEvent := <-newBlockCh:
			clearPending(blockEvent.Block.Number64())
			timestamp = time.Now().Unix()
			commit(false, commitInterruptNewHead)
		case err := <-newBlockSub.Err():
			return err

		case <-timer.C:
			// If sealing is running resubmit a new work cycle periodically to pull in
			// higher priced transactions. Disable this overhead for pending blocks.
			//if w.isRunning() && (w.chainConfig.Apos == nil && w.chainConfig.Clique == nil) {
			//	continue
			//	commit(false, commitInterruptResubmit)
			//}
		case adjust := <-w.resubmitAdjustCh:
			// Adjust resubmit interval by feedback.
			if adjust.inc {
				before := recommit
				target := float64(recommit.Nanoseconds()) / adjust.ratio
				recommit = recalcRecommit(minRecommit, recommit, target, true)
				log.Trace("Increase miner recommit interval", "from", before, "to", recommit)
			} else {
				before := recommit
				recommit = recalcRecommit(minRecommit, recommit, float64(minRecommit.Nanoseconds()), false)
				log.Trace("Decrease miner recommit interval", "from", before, "to", recommit)
			}
		}
	}
}

func (w *worker) fillTransactions(interrupt *atomic.Int32, env *environment, ibs *state.IntraBlockState, getHeader func(hash types.Hash, number uint64) *block.Header) error {
	// todo fillTx
	env.txs = []*transaction.Transaction{}
	txs, err := w.txsPool.GetTransaction()
	if err != nil {
		log.Warn("get transaction error", "err", err)
		return err
	}

	header := env.header
	noop := state.NewNoopWriter()
	var miningCommitTx = func(txn *transaction.Transaction, coinbase types.Address, vmConfig *vm2.Config, chainConfig *params.ChainConfig, ibs *state.IntraBlockState, current *environment) ([]*block.Log, error) {
		ibs.Prepare(txn.Hash(), types.Hash{}, env.tcount)
		gasSnap := current.gasPool.Gas()
		snap := ibs.Snapshot()
		log.Debug("addTransactionsToMiningBlock", "txn hash", txn.Hash())
		receipt, _, err := internal.ApplyTransaction(chainConfig, internal.GetHashFn(header, getHeader), w.engine, &coinbase, env.gasPool, ibs, noop, current.header, txn, &header.GasUsed, *vmConfig)
		if err != nil {
			ibs.RevertToSnapshot(snap)
			env.gasPool = new(common.GasPool).AddGas(gasSnap) // restore gasPool as well as ibs
			return nil, err
		}

		current.txs = append(current.txs, txn)
		current.receipts = append(current.receipts, receipt)
		return receipt.Logs, nil
	}

	log.Tracef("fillTransactions txs len:%d", len(txs))
	for _, tx := range txs {
		// Check interruption signal and abort building if it's fired.
		if interrupt != nil {
			if signal := interrupt.Load(); signal != commitInterruptNone {
				return signalToErr(signal)
			}
		}
		if env.gasPool.Gas() < params.TxGas {
			log.Trace("Not enough gas for further transactions", "have", env.gasPool, "want", params.TxGas)
			break
		}
		// Start executing the transaction
		_, err := miningCommitTx(tx, env.coinbase, &vm2.Config{}, w.chainConfig, ibs, env)

		switch {
		case errors.Is(err, core.ErrGasLimitReached):
			continue
		case errors.Is(err, core.ErrNonceTooHigh):
			continue
		case errors.Is(err, core.ErrNonceTooLow):
			continue
		case errors.Is(err, nil):
			env.tcount++
			continue
		default:
			log.Error("miningCommitTx failed ", "error", err)
		}
	}

	return nil
}

func (w *worker) prepareWork(param *generateParams) (*environment, error) {
	w.mu.RLock()
	defer w.mu.RUnlock()

	timestamp := param.timestamp

	parent := w.chain.CurrentBlock().Header().(*block.Header)
	if param.parentHash != (types.Hash{}) {
		b, _ := w.chain.GetBlockByHash(param.parentHash)
		if b == nil {
			return nil, fmt.Errorf("missing parent")
		}
		parent = b.Header().(*block.Header)
	}

	if parent.Time >= param.timestamp {
		timestamp = parent.Time + 1
	}

	header := &block.Header{
		//Root:       parent.StateRoot(),
		ParentHash: parent.Hash(),
		Coinbase:   param.coinbase,
		Number:     uint256.NewInt(0).Add(parent.Number64(), uint256.NewInt(1)),
		GasLimit:   CalcGasLimit(parent.GasLimit, w.minerConf.GasCeil),
		Time:       uint64(timestamp),
		Difficulty: uint256.NewInt(0),
		// just for now
		BaseFee: uint256.NewInt(0),
	}

	// Set baseFee and GasLimit if we are on an EIP-1559 chain
	if w.chainConfig.IsLondon(header.Number.Uint64()) {
		header.BaseFee, _ = uint256.FromBig(misc.CalcBaseFee(w.chainConfig, parent))
		if !w.chainConfig.IsLondon(parent.Number64().Uint64()) {
			parentGasLimit := parent.GasLimit * params.ElasticityMultiplier
			header.GasLimit = CalcGasLimit(parentGasLimit, w.minerConf.GasCeil)
		}
	}

	if err := w.engine.Prepare(w.chain, header); err != nil {
		return nil, err
	}

	return w.makeEnv(parent, header, param.coinbase), nil
}

func (w *worker) makeEnv(parent *block.Header, header *block.Header, coinbase types.Address) *environment {
	//rtx, err := w.chain.DB().BeginRo(context.Background())
	//if nil != err {
	//	return nil
	//}
	//defer rtx.Rollback()
	env := &environment{
		ancestors: mapset.NewSet(),
		family:    mapset.NewSet(),
		coinbase:  coinbase,
		header:    header,
		//state:     ibs,
		gasPool: new(common.GasPool),
		tcount:  0,
	}

	env.gasPool = new(common.GasPool).AddGas(header.GasLimit)
	//}

	for _, ancestor := range w.chain.GetBlocksFromHash(parent.ParentHash, 3) {
		env.family.Add(ancestor.(*block.Block).Hash())
		env.ancestors.Add(ancestor.Hash())
	}

	return env
}

func (w *worker) commit(env *environment, writer state.WriterWithChangeSets, ibs *state.IntraBlockState, start time.Time, needHeaders []*block.Header) error {
	if w.isRunning() {
		env := env.copy()
		iblock, rewards, unpay, err := w.engine.FinalizeAndAssemble(w.chain, env.header, ibs, env.txs, nil, env.receipts)
		if nil != err {
			return err
		}

		if w.chainConfig.IsBeijing(env.header.Number.Uint64()) {
			txs := make([][]byte, len(env.txs))
			for i, tx := range env.txs {
				var err error

				txs[i], err = tx.Marshal()
				if nil != err {
					panic(err)
				}
			}

			entri := state.Entire{Header: iblock.Header().(*block.Header), Uncles: nil, Transactions: txs, Senders: nil, Snap: ibs.Snap(), Proof: types.Hash{}}
			cs := ibs.CodeHashes()
			hs := make(state.HashCodes, 0, len(cs))
			for k, v := range cs {
				hs = append(hs, &state.HashCode{Hash: k, Code: v})
			}
			sort.Sort(hs)

			event.GlobalEvent.Send(common.MinedEntireEvent{Entire: state.EntireCode{Codes: hs, Headers: needHeaders, Entire: entri, Rewards: rewards, CoinBase: env.coinbase}})
		}

		//
		w.updateSnapshot(env, rewards)

		select {
		case w.taskCh <- &task{receipts: env.receipts, block: iblock, createdAt: time.Now(), state: ibs, nopay: unpay}:
			log.Debug("Commit new sealing work",
				"number", iblock.Header().Number64().Uint64(),
				"sealhash", w.engine.SealHash(iblock.Header()),
				"txs", env.tcount,
				"gas", iblock.GasUsed(),
				"elapsed", common.PrettyDuration(time.Since(start)),
				"headerTime", time.Unix(int64(iblock.Time()), 0).Format(time.RFC3339),
				"rewardCount", len(iblock.Body().Reward()),
			)
		case <-w.ctx.Done():
			return w.ctx.Err()
		}
	}
	return nil
}

// copyReceipts makes a deep copy of the given receipts.
func copyReceipts(receipts []*block.Receipt) []*block.Receipt {
	result := make([]*block.Receipt, len(receipts))
	for i, l := range receipts {
		cpy := *l
		result[i] = &cpy
	}
	return result
}

// pendingBlockAndReceipts returns pending block and corresponding receipts.
func (w *worker) pendingBlockAndReceipts() (block.IBlock, block.Receipts) {
	// return a snapshot to avoid contention on currentMu mutex
	w.snapshotMu.RLock()
	defer w.snapshotMu.RUnlock()
	return w.snapshotBlock, w.snapshotReceipts
}

// updateSnapshot updates pending snapshot block, receipts and state.
func (w *worker) updateSnapshot(env *environment, rewards []*block.Reward) {
	w.snapshotMu.Lock()
	defer w.snapshotMu.Unlock()

	w.snapshotBlock = block.NewBlockFromReceipt(
		env.header,
		env.txs,
		nil,
		env.receipts,
		rewards,
	)
	w.snapshotReceipts = copyReceipts(env.receipts)
}

func signalToErr(signal int32) error {
	switch signal {
	case commitInterruptNewHead:
		return errBlockInterruptedByNewHead
	case commitInterruptResubmit:
		return errBlockInterruptedByRecommit
	case commitInterruptTimeout:
		return errBlockInterruptedByTimeout
	default:
		panic(fmt.Errorf("undefined signal %d", signal))
	}
}
