// Copyright 2023 The N42 Authors
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
// Package core implements the Ethereum consensus protocol.

package internal

import (
	"fmt"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/math"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/common/u256"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/internal/consensus/misc"
	"github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/internal/vm/evmtypes"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

//var (
//	BlockExecutionTimer = metrics2.GetOrCreateSummary("chain_execution_seconds")
//)

type SyncMode string

const (
	TriesInMemory = 128
)

type RejectedTx struct {
	Index int    `json:"index"    gencodec:"required"`
	Err   string `json:"error"    gencodec:"required"`
}

type RejectedTxs []*RejectedTx

type EphemeralExecResult struct {
	StateRoot        types.Hash            `json:"stateRoot"`
	TxRoot           types.Hash            `json:"txRoot"`
	ReceiptRoot      types.Hash            `json:"receiptsRoot"`
	LogsHash         types.Hash            `json:"logsHash"`
	Bloom            types.Bloom           `json:"logsBloom"        gencodec:"required"`
	Receipts         block.Receipts        `json:"receipts"`
	Rejected         RejectedTxs           `json:"rejected,omitempty"`
	Difficulty       *math.HexOrDecimal256 `json:"currentDifficulty" gencodec:"required"`
	GasUsed          math.HexOrDecimal64   `json:"gasUsed"`
	StateSyncReceipt *block.Receipt        `json:"-"`
}

// ExecuteBlockEphemerally runs a block from provided stateReader and
// writes the result to the provided stateWriter
//func ExecuteBlockEphemerally(
//	chainConfig *params.ChainConfig,
//	vmConfig *vm.Config,
//	blockHashFunc func(n uint64) types.Hash,
//	engine consensus.Engine,
//	block *block.Block,
//	stateReader state.StateReader,
//	stateWriter state.WriterWithChangeSets,
//	epochReader consensus.EpochReader,
//	chainReader consensus.ChainHeaderReader,
//	getTracer func(txIndex int, txHash types.Hash) (vm.Tracer, error),
//) (*EphemeralExecResult, error) {
//
//	defer BlockExecutionTimer.UpdateDuration(time.Now())
//	block.Uncles()
//	ibs := state.New(stateReader)
//	header := block.Header()
//
//	usedGas := new(uint64)
//	gp := new(GasPool)
//	gp.AddGas(block.GasLimit())
//
//	var (
//		rejectedTxs []*RejectedTx
//		includedTxs types.Transactions
//		receipts    types.Receipts
//	)
//
//	if !vmConfig.ReadOnly {
//		if err := InitializeBlockExecution(engine, chainReader, epochReader, block.Header(), block.Transactions(), block.Uncles(), chainConfig, ibs); err != nil {
//			return nil, err
//		}
//	}
//
//	if chainConfig.DAOForkSupport && chainConfig.DAOForkBlock != nil && chainConfig.DAOForkBlock.Cmp(block.Number()) == 0 {
//		misc.ApplyDAOHardFork(ibs)
//	}
//	noop := state.NewNoopWriter()
//	//fmt.Printf("====txs processing start: %d====\n", block.NumberU64())
//	for i, tx := range block.Transactions() {
//		ibs.Prepare(tx.Hash(), block.Hash(), i)
//		writeTrace := false
//		if vmConfig.Debug && vmConfig.Tracer == nil {
//			tracer, err := getTracer(i, tx.Hash())
//			if err != nil {
//				return nil, fmt.Errorf("could not obtain tracer: %w", err)
//			}
//			vmConfig.Tracer = tracer
//			writeTrace = true
//		}
//
//		receipt, _, err := ApplyTransaction(chainConfig, blockHashFunc, engine, nil, gp, ibs, noop, header, tx, usedGas, *vmConfig)
//		if writeTrace {
//			if ftracer, ok := vmConfig.Tracer.(vm.FlushableTracer); ok {
//				ftracer.Flush(tx)
//			}
//
//			vmConfig.Tracer = nil
//		}
//		if err != nil {
//			if !vmConfig.StatelessExec {
//				return nil, fmt.Errorf("could not apply tx %d from block %d [%v]: %w", i, block.NumberU64(), tx.Hash().Hex(), err)
//			}
//			rejectedTxs = append(rejectedTxs, &RejectedTx{i, err.Error()})
//		} else {
//			includedTxs = append(includedTxs, tx)
//			if !vmConfig.NoReceipts {
//				receipts = append(receipts, receipt)
//			}
//		}
//	}
//
//	receiptSha := types.DeriveSha(receipts)
//	if !vmConfig.StatelessExec && chainConfig.IsByzantium(header.Number.Uint64()) && !vmConfig.NoReceipts && receiptSha != block.ReceiptHash() {
//		return nil, fmt.Errorf("mismatched receipt headers for block %d (%s != %s)", block.NumberU64(), receiptSha.Hex(), block.ReceiptHash().Hex())
//	}
//
//	if !vmConfig.StatelessExec && *usedGas != header.GasUsed {
//		return nil, fmt.Errorf("gas used by execution: %d, in header: %d", *usedGas, header.GasUsed)
//	}
//
//	var bloom types.Bloom
//	if !vmConfig.NoReceipts {
//		bloom = types.CreateBloom(receipts)
//		if !vmConfig.StatelessExec && bloom != header.Bloom {
//			return nil, fmt.Errorf("bloom computed by execution: %x, in header: %x", bloom, header.Bloom)
//		}
//	}
//	if !vmConfig.ReadOnly {
//		txs := block.Transactions()
//		if _, _, _, err := FinalizeBlockExecution(engine, stateReader, block.Header(), txs, block.Uncles(), stateWriter, chainConfig, ibs, receipts, epochReader, chainReader, false); err != nil {
//			return nil, err
//		}
//	}
//	blockLogs := ibs.Logs()
//	execRs := &EphemeralExecResult{
//		TxRoot:      types.DeriveSha(includedTxs),
//		ReceiptRoot: receiptSha,
//		Bloom:       bloom,
//		LogsHash:    rlpHash(blockLogs),
//		Receipts:    receipts,
//		Difficulty:  (*math.HexOrDecimal256)(header.Difficulty),
//		GasUsed:     math.HexOrDecimal64(*usedGas),
//		Rejected:    rejectedTxs,
//	}
//
//	return execRs, nil
//}

// ExecuteBlockEphemerallyBor runs a block from provided stateReader and
// writes the result to the provided stateWriter
//func ExecuteBlockEphemerallyBor(
//	chainConfig *params.ChainConfig,
//	vmConfig *vm.Config,
//	blockHashFunc func(n uint64) types.Hash,
//	engine consensus.Engine,
//	block *types.Block,
//	stateReader state.StateReader,
//	stateWriter state.WriterWithChangeSets,
//	epochReader consensus.EpochReader,
//	chainReader consensus.ChainHeaderReader,
//	getTracer func(txIndex int, txHash types.Hash) (vm.Tracer, error),
//) (*EphemeralExecResult, error) {
//
//	defer BlockExecutionTimer.UpdateDuration(time.Now())
//	block.Uncles()
//	ibs := state.New(stateReader)
//	header := block.Header()
//
//	usedGas := new(uint64)
//	gp := new(GasPool)
//	gp.AddGas(block.GasLimit())
//
//	var (
//		rejectedTxs []*RejectedTx
//		includedTxs types.Transactions
//		receipts    types.Receipts
//	)
//
//	if !vmConfig.ReadOnly {
//		if err := InitializeBlockExecution(engine, chainReader, epochReader, block.Header(), block.Transactions(), block.Uncles(), chainConfig, ibs); err != nil {
//			return nil, err
//		}
//	}
//
//	if chainConfig.DAOForkSupport && chainConfig.DAOForkBlock != nil && chainConfig.DAOForkBlock.Cmp(block.Number()) == 0 {
//		misc.ApplyDAOHardFork(ibs)
//	}
//	noop := state.NewNoopWriter()
//	//fmt.Printf("====txs processing start: %d====\n", block.NumberU64())
//	for i, tx := range block.Transactions() {
//		ibs.Prepare(tx.Hash(), block.Hash(), i)
//		writeTrace := false
//		if vmConfig.Debug && vmConfig.Tracer == nil {
//			tracer, err := getTracer(i, tx.Hash())
//			if err != nil {
//				return nil, fmt.Errorf("could not obtain tracer: %w", err)
//			}
//			vmConfig.Tracer = tracer
//			writeTrace = true
//		}
//
//		receipt, _, err := ApplyTransaction(chainConfig, blockHashFunc, engine, nil, gp, ibs, noop, header, tx, usedGas, *vmConfig)
//		if writeTrace {
//			if ftracer, ok := vmConfig.Tracer.(vm.FlushableTracer); ok {
//				ftracer.Flush(tx)
//			}
//
//			vmConfig.Tracer = nil
//		}
//		if err != nil {
//			if !vmConfig.StatelessExec {
//				return nil, fmt.Errorf("could not apply tx %d from block %d [%v]: %w", i, block.NumberU64(), tx.Hash().Hex(), err)
//			}
//			rejectedTxs = append(rejectedTxs, &RejectedTx{i, err.Error()})
//		} else {
//			includedTxs = append(includedTxs, tx)
//			if !vmConfig.NoReceipts {
//				receipts = append(receipts, receipt)
//			}
//		}
//	}
//
//	receiptSha := types.DeriveSha(receipts)
//	if !vmConfig.StatelessExec && chainConfig.IsByzantium(header.Number.Uint64()) && !vmConfig.NoReceipts && receiptSha != block.ReceiptHash() {
//		return nil, fmt.Errorf("mismatched receipt headers for block %d (%s != %s)", block.NumberU64(), receiptSha.Hex(), block.ReceiptHash().Hex())
//	}
//
//	if !vmConfig.StatelessExec && *usedGas != header.GasUsed {
//		return nil, fmt.Errorf("gas used by execution: %d, in header: %d", *usedGas, header.GasUsed)
//	}
//
//	var bloom types.Bloom
//	if !vmConfig.NoReceipts {
//		bloom = types.CreateBloom(receipts)
//		if !vmConfig.StatelessExec && bloom != header.Bloom {
//			return nil, fmt.Errorf("bloom computed by execution: %x, in header: %x", bloom, header.Bloom)
//		}
//	}
//	if !vmConfig.ReadOnly {
//		txs := block.Transactions()
//		if _, _, _, err := FinalizeBlockExecution(engine, stateReader, block.Header(), txs, block.Uncles(), stateWriter, chainConfig, ibs, receipts, epochReader, chainReader, false); err != nil {
//			return nil, err
//		}
//	}
//
//	var logs []*types.Log
//	for _, receipt := range receipts {
//		logs = append(logs, receipt.Logs...)
//	}
//
//	blockLogs := ibs.Logs()
//	stateSyncReceipt := &types.Receipt{}
//	if chainConfig.Consensus == params.BorConsensus && len(blockLogs) > 0 {
//		slices.SortStableFunc(blockLogs, func(i, j *types.Log) bool { return i.Index < j.Index })
//
//		if len(blockLogs) > len(logs) {
//			stateSyncReceipt.Logs = blockLogs[len(logs):] // get state-sync logs from `state.Logs()`
//
//			// fill the state sync with the correct information
//			types.DeriveFieldsForBorReceipt(stateSyncReceipt, block.Hash(), block.NumberU64(), receipts)
//			stateSyncReceipt.Status = types.ReceiptStatusSuccessful
//		}
//	}
//
//	execRs := &EphemeralExecResult{
//		TxRoot:           types.DeriveSha(includedTxs),
//		ReceiptRoot:      receiptSha,
//		Bloom:            bloom,
//		LogsHash:         rlpHash(blockLogs),
//		Receipts:         receipts,
//		Difficulty:       (*math.HexOrDecimal256)(header.Difficulty),
//		GasUsed:          math.HexOrDecimal64(*usedGas),
//		Rejected:         rejectedTxs,
//		StateSyncReceipt: stateSyncReceipt,
//	}
//
//	return execRs, nil
//}

func SysCallContract(contract types.Address, data []byte, chainConfig params.ChainConfig, ibs *state.IntraBlockState, header *block.Header, engine consensus.Engine) (result []byte, err error) {
	if chainConfig.DAOForkSupport && chainConfig.DAOForkBlock != nil && chainConfig.DAOForkBlock.Cmp(header.Number64().ToBig()) == 0 {
		misc.ApplyDAOHardFork(ibs)
	}

	msg := transaction.NewMessage(
		state.SystemAddress,
		&contract,
		0, u256.Num0,
		math.MaxUint64, u256.Num0,
		nil, nil,
		data, nil, false,
		true, // isFree
	)
	vmConfig := vm.Config{NoReceipts: true}
	// Create a new context to be used in the EVM environment
	isBor := chainConfig.Bor != nil
	var txContext evmtypes.TxContext
	var author *types.Address
	if isBor {
		author = &header.Coinbase
		txContext = evmtypes.TxContext{}
	} else {
		author = &state.SystemAddress
		txContext = NewEVMTxContext(msg)
	}
	blockContext := NewEVMBlockContext(header, GetHashFn(header, nil), engine, author)
	evm := vm.NewEVM(blockContext, txContext, ibs, &chainConfig, vmConfig)

	ret, _, err := evm.Call(
		vm.AccountRef(msg.From()),
		*msg.To(),
		msg.Data(),
		msg.Gas(),
		msg.Value(),
		false,
	)
	if isBor && err != nil {
		return nil, nil
	}
	return ret, err
}

// from the null sender, with 50M gas.
//func SysCallContractTx(contract types.Address, data []byte) (tx transaction.Transaction, err error) {
//	//nonce := ibs.GetNonce(SystemAddress)
//	tx = transaction.NewTransaction(0, contract, u256.Num0, 50_000_000, u256.Num0, data)
//	return tx.FakeSign(state.SystemAddress)
//}

//func CallContract(contract types.Address, data []byte, chainConfig params.ChainConfig, ibs *state.IntraBlockState, header *block.Header, engine consensus.Engine) (result []byte, err error) {
//	gp := new(common.GasPool)
//	gp.AddGas(50_000_000)
//	var gasUsed uint64
//
//	if chainConfig.DAOForkSupport && chainConfig.DAOForkBlock != nil && chainConfig.DAOForkBlock.Cmp(header.Number64().ToBig()) == 0 {
//		misc.ApplyDAOHardFork(ibs)
//	}
//	noop := state.NewNoopWriter()
//	tx, err := CallContractTx(contract, data, ibs)
//	if err != nil {
//		return nil, fmt.Errorf("SysCallContract: %w ", err)
//	}
//	vmConfig := vm.Config{NoReceipts: true}
//	_, result, err = ApplyTransaction(&chainConfig, GetHashFn(header, nil), engine, &state.SystemAddress, gp, ibs, noop, header, tx, &gasUsed, vmConfig)
//	if err != nil {
//		return result, fmt.Errorf("SysCallContract: %w ", err)
//	}
//	return result, nil
//}

//// from the null sender, with 50M gas.
//func CallContractTx(contract types.Address, data []byte, ibs *state.IntraBlockState) (tx transaction.Transaction, err error) {
//	from := types.Address{}
//	nonce := ibs.GetNonce(from)
//	tx = transaction.NewTransaction(nonce, contract, u256.Num0, 50_000_000, u256.Num0, data)
//	return tx.FakeSign(from)
//}

func FinalizeBlockExecution(engine consensus.Engine, header *block.Header,
	txs transaction.Transactions, stateWriter state.WriterWithChangeSets, cc *params.ChainConfig, ibs *state.IntraBlockState,
	receipts block.Receipts, headerReader consensus.ChainHeaderReader, isMining bool) (newBlock block.IBlock, newTxs transaction.Transactions, newReceipt block.Receipts, err error) {
	//syscall := func(contract types.Address, data []byte) ([]byte, error) {
	//	return SysCallContract(contract, data, *cc, ibs, header, engine)
	//}

	if isMining {
		newBlock, _, _, err = engine.FinalizeAndAssemble(headerReader, header, ibs, txs, nil, receipts)
	} else {
		_, _, err = engine.Finalize(headerReader, header, ibs, txs, nil)
	}
	if err != nil {
		return nil, nil, nil, err
	}

	if err := ibs.CommitBlock(cc.Rules(header.Number.Uint64()), stateWriter); err != nil {
		return nil, nil, nil, fmt.Errorf("committing block %d failed: %w", header.Number.Uint64(), err)
	}

	if err := stateWriter.WriteChangeSets(); err != nil {
		return nil, nil, nil, fmt.Errorf("writing changesets for block %d failed: %w", header.Number.Uint64(), err)
	}

	if err := stateWriter.WriteHistory(); err != nil {
		return nil, nil, nil, fmt.Errorf("writing history for block %d failed: %w", header.Number.Uint64(), err)
	}

	return newBlock, newTxs, newReceipt, nil
}

//func InitializeBlockExecution(engine consensus.Engine, chain consensus.ChainHeaderReader, header *block.Header, txs transaction.Transactions, uncles []*block.Header, cc *params.ChainConfig, ibs *state.IntraBlockState) error {
//	//engine.Initialize(cc, chain, header, txs, uncles, func(contract types.Address, data []byte) ([]byte, error) {
//	//	return SysCallContract(contract, data, *cc, ibs, header, engine)
//	//})
//	noop := state.NewNoopWriter()
//	ibs.FinalizeTx(cc.Rules(header.Number.Uint64()), noop)
//	return nil
//}
