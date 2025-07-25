// Copyright 2024 The Erigon Authors
// This file is part of Erigon.
//
// Erigon is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// Erigon is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with Erigon. If not, see <http://www.gnu.org/licenses/>.

package jsonrpc

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/erigontech/erigon-lib/common"
	"github.com/erigontech/erigon-lib/common/datadir"
	"github.com/erigontech/erigon-lib/common/hexutil"
	"github.com/erigontech/erigon-lib/kv/kvcache"
	"github.com/erigontech/erigon-lib/log/v3"
	"github.com/erigontech/erigon-lib/types"
	"github.com/erigontech/erigon/cmd/rpcdaemon/rpcdaemontest"
	"github.com/erigontech/erigon/core"
	"github.com/erigontech/erigon/eth/ethconfig"
	"github.com/erigontech/erigon/execution/stages/mock"
	"github.com/erigontech/erigon/rpc"
	"github.com/erigontech/erigon/rpc/ethapi"
	"github.com/erigontech/erigon/rpc/rpccfg"
	"github.com/erigontech/erigon/turbo/snapshotsync/freezeblocks"
)

func newBaseApiForTest(m *mock.MockSentry) *BaseAPI {
	stateCache := kvcache.New(kvcache.DefaultCoherentConfig)
	return NewBaseApi(nil, stateCache, m.BlockReader, false, rpccfg.DefaultEvmCallTimeout, m.Engine, m.Dirs, nil)
}

func TestGetBalanceChangesInBlock(t *testing.T) {
	assert := assert.New(t)
	myBlockNum := rpc.BlockNumberOrHashWithNumber(0)
	m, _, _ := rpcdaemontest.CreateTestSentry(t)
	db := m.DB
	api := NewErigonAPI(newBaseApiForTest(m), db, nil)
	balances, err := api.GetBalanceChangesInBlock(context.Background(), myBlockNum)
	if err != nil {
		t.Errorf("calling GetBalanceChangesInBlock resulted in an error: %v", err)
	}
	expected := map[common.Address]*hexutil.Big{
		common.HexToAddress("0x0D3ab14BBaD3D99F4203bd7a11aCB94882050E7e"): (*hexutil.Big)(uint256.NewInt(200000000000000000).ToBig()),
		common.HexToAddress("0x703c4b2bD70c169f5717101CaeE543299Fc946C7"): (*hexutil.Big)(uint256.NewInt(300000000000000000).ToBig()),
		common.HexToAddress("0x71562b71999873DB5b286dF957af199Ec94617F7"): (*hexutil.Big)(uint256.NewInt(9000000000000000000).ToBig()),
	}
	assert.Len(balances, len(expected))
	for i := range balances {
		assert.Contains(expected, i, "%s is not expected to be present in the output.", i)
		assert.Equal(balances[i], expected[i], "the value for %s is expected to be %v, but got %v.", i, expected[i], balances[i])
	}
}

func TestGetTransactionReceipt(t *testing.T) {
	m, _, _ := rpcdaemontest.CreateTestSentry(t)
	db := m.DB
	stateCache := kvcache.New(kvcache.DefaultCoherentConfig)
	api := NewEthAPI(NewBaseApi(nil, stateCache, m.BlockReader, false, rpccfg.DefaultEvmCallTimeout, m.Engine, m.Dirs, nil), db, nil, nil, nil, 5000000, ethconfig.Defaults.RPCTxFeeCap, 100_000, false, 100_000, 128, log.New())
	// Call GetTransactionReceipt for transaction which is not in the database
	if _, err := api.GetTransactionReceipt(context.Background(), common.Hash{}); err != nil {
		t.Errorf("calling GetTransactionReceipt with empty hash: %v", err)
	}
}

func TestGetTransactionReceiptUnprotected(t *testing.T) {
	m, _, _ := rpcdaemontest.CreateTestSentry(t)
	api := NewEthAPI(newBaseApiForTest(m), m.DB, nil, nil, nil, 5000000, ethconfig.Defaults.RPCTxFeeCap, 100_000, false, 100_000, 128, log.New())
	// Call GetTransactionReceipt for un-protected transaction
	if _, err := api.GetTransactionReceipt(context.Background(), common.HexToHash("0x3f3cb8a0e13ed2481f97f53f7095b9cbc78b6ffb779f2d3e565146371a8830ea")); err != nil {
		t.Errorf("calling GetTransactionReceipt for unprotected tx: %v", err)
	}
}

// EIP-1898 test cases

func TestGetStorageAt_ByBlockNumber_WithRequireCanonicalDefault(t *testing.T) {
	assert := assert.New(t)
	m, _, _ := rpcdaemontest.CreateTestSentry(t)
	api := NewEthAPI(newBaseApiForTest(m), m.DB, nil, nil, nil, 5000000, ethconfig.Defaults.RPCTxFeeCap, 100_000, false, 100_000, 128, log.New())
	addr := common.HexToAddress("0x71562b71999873db5b286df957af199ec94617f7")

	result, err := api.GetStorageAt(context.Background(), addr, "0x0", rpc.BlockNumberOrHashWithNumber(0))
	if err != nil {
		t.Errorf("calling GetStorageAt: %v", err)
	}

	assert.Equal(common.HexToHash("0x0").String(), result)
}

func TestGetStorageAt_ByBlockHash_WithRequireCanonicalDefault(t *testing.T) {
	assert := assert.New(t)
	m, _, _ := rpcdaemontest.CreateTestSentry(t)
	api := NewEthAPI(newBaseApiForTest(m), m.DB, nil, nil, nil, 5000000, ethconfig.Defaults.RPCTxFeeCap, 100_000, false, 100_000, 128, log.New())
	addr := common.HexToAddress("0x71562b71999873db5b286df957af199ec94617f7")

	result, err := api.GetStorageAt(context.Background(), addr, "0x0", rpc.BlockNumberOrHashWithHash(m.Genesis.Hash(), false))
	if err != nil {
		t.Errorf("calling GetStorageAt: %v", err)
	}

	assert.Equal(common.HexToHash("0x0").String(), result)
}

func TestGetStorageAt_ByBlockHash_WithRequireCanonicalTrue(t *testing.T) {
	assert := assert.New(t)
	m, _, _ := rpcdaemontest.CreateTestSentry(t)
	api := NewEthAPI(newBaseApiForTest(m), m.DB, nil, nil, nil, 5000000, ethconfig.Defaults.RPCTxFeeCap, 100_000, false, 100_000, 128, log.New())
	addr := common.HexToAddress("0x71562b71999873db5b286df957af199ec94617f7")

	result, err := api.GetStorageAt(context.Background(), addr, "0x0", rpc.BlockNumberOrHashWithHash(m.Genesis.Hash(), true))
	if err != nil {
		t.Errorf("calling GetStorageAt: %v", err)
	}

	assert.Equal(common.HexToHash("0x0").String(), result)
}

func TestGetStorageAt_ByBlockHash_WithRequireCanonicalDefault_BlockNotFoundError(t *testing.T) {
	m, _, _ := rpcdaemontest.CreateTestSentry(t)
	api := NewEthAPI(newBaseApiForTest(m), m.DB, nil, nil, nil, 5000000, ethconfig.Defaults.RPCTxFeeCap, 100_000, false, 100_000, 128, log.New())
	addr := common.HexToAddress("0x71562b71999873db5b286df957af199ec94617f7")

	offChain, err := core.GenerateChain(m.ChainConfig, m.Genesis, m.Engine, m.DB, 1, func(i int, block *core.BlockGen) {
	})
	if err != nil {
		t.Fatal(err)
	}
	offChainBlock := offChain.Blocks[0]

	if _, err := api.GetStorageAt(context.Background(), addr, "0x0", rpc.BlockNumberOrHashWithHash(offChainBlock.Hash(), false)); err != nil {
		if fmt.Sprintf("%v", err) != fmt.Sprintf("block %s not found", offChainBlock.Hash().String()[2:]) {
			t.Errorf("wrong error: %v", err)
		}
	} else {
		t.Error("error expected")
	}
}

func TestGetStorageAt_ByBlockHash_WithRequireCanonicalTrue_BlockNotFoundError(t *testing.T) {
	m, _, _ := rpcdaemontest.CreateTestSentry(t)
	api := NewEthAPI(newBaseApiForTest(m), m.DB, nil, nil, nil, 5000000, ethconfig.Defaults.RPCTxFeeCap, 100_000, false, 100_000, 128, log.New())
	addr := common.HexToAddress("0x71562b71999873db5b286df957af199ec94617f7")

	offChain, err := core.GenerateChain(m.ChainConfig, m.Genesis, m.Engine, m.DB, 1, func(i int, block *core.BlockGen) {
	})
	if err != nil {
		t.Fatal(err)
	}
	offChainBlock := offChain.Blocks[0]

	if _, err := api.GetStorageAt(context.Background(), addr, "0x0", rpc.BlockNumberOrHashWithHash(offChainBlock.Hash(), true)); err != nil {
		if fmt.Sprintf("%v", err) != fmt.Sprintf("block %s not found", offChainBlock.Hash().String()[2:]) {
			t.Errorf("wrong error: %v", err)
		}
	} else {
		t.Error("error expected")
	}
}

func TestGetStorageAt_ByBlockHash_WithRequireCanonicalDefault_NonCanonicalBlock(t *testing.T) {
	assert := assert.New(t)
	m, _, orphanedChain := rpcdaemontest.CreateTestSentry(t)
	api := NewEthAPI(newBaseApiForTest(m), m.DB, nil, nil, nil, 5000000, ethconfig.Defaults.RPCTxFeeCap, 100_000, false, 100_000, 128, log.New())
	addr := common.HexToAddress("0x71562b71999873db5b286df957af199ec94617f7")

	orphanedBlock := orphanedChain[0].Blocks[0]

	result, err := api.GetStorageAt(context.Background(), addr, "0x0", rpc.BlockNumberOrHashWithHash(orphanedBlock.Hash(), false))
	if err != nil {
		if fmt.Sprintf("%v", err) != fmt.Sprintf("hash %s is not currently canonical", orphanedBlock.Hash().String()[2:]) {
			t.Errorf("wrong error: %v", err)
		}
	} else {
		t.Error("error expected")
	}

	assert.Equal(common.HexToHash("0x0").String(), result)
}

func TestGetStorageAt_ByBlockHash_WithRequireCanonicalTrue_NonCanonicalBlock(t *testing.T) {
	m, _, orphanedChain := rpcdaemontest.CreateTestSentry(t)
	api := NewEthAPI(newBaseApiForTest(m), m.DB, nil, nil, nil, 5000000, ethconfig.Defaults.RPCTxFeeCap, 100_000, false, 100_000, 128, log.New())
	addr := common.HexToAddress("0x71562b71999873db5b286df957af199ec94617f7")

	orphanedBlock := orphanedChain[0].Blocks[0]

	if _, err := api.GetStorageAt(context.Background(), addr, "0x0", rpc.BlockNumberOrHashWithHash(orphanedBlock.Hash(), true)); err != nil {
		if fmt.Sprintf("%v", err) != fmt.Sprintf("hash %s is not currently canonical", orphanedBlock.Hash().String()[2:]) {
			t.Errorf("wrong error: %v", err)
		}
	} else {
		t.Error("error expected")
	}
}

func TestCall_ByBlockHash_WithRequireCanonicalDefault_NonCanonicalBlock(t *testing.T) {
	m, _, orphanedChain := rpcdaemontest.CreateTestSentry(t)
	api := NewEthAPI(newBaseApiForTest(m), m.DB, nil, nil, nil, 5000000, ethconfig.Defaults.RPCTxFeeCap, 100_000, false, 100_000, 128, log.New())
	from := common.HexToAddress("0x71562b71999873db5b286df957af199ec94617f7")
	to := common.HexToAddress("0x0d3ab14bbad3d99f4203bd7a11acb94882050e7e")

	orphanedBlock := orphanedChain[0].Blocks[0]

	blockNumberOrHash := rpc.BlockNumberOrHashWithHash(orphanedBlock.Hash(), false)
	var blockNumberOrHashRef *rpc.BlockNumberOrHash = &blockNumberOrHash

	if _, err := api.Call(context.Background(), ethapi.CallArgs{
		From: &from,
		To:   &to,
	}, blockNumberOrHashRef, nil); err != nil {
		if fmt.Sprintf("%v", err) != fmt.Sprintf("hash %s is not currently canonical", orphanedBlock.Hash().String()[2:]) {
			/* Not sure. Here https://github.com/ethereum/EIPs/blob/master/EIPS/eip-1898.md it is not explicitly said that
			   eth_call should only work with canonical blocks.
			   But since there is no point in changing the state of non-canonical block, it ignores RequireCanonical. */
			t.Errorf("wrong error: %v", err)
		}
	} else {
		t.Error("error expected")
	}
}

func TestCall_ByBlockHash_WithRequireCanonicalTrue_NonCanonicalBlock(t *testing.T) {
	m, _, orphanedChain := rpcdaemontest.CreateTestSentry(t)
	api := NewEthAPI(newBaseApiForTest(m), m.DB, nil, nil, nil, 5000000, ethconfig.Defaults.RPCTxFeeCap, 100_000, false, 100_000, 128, log.New())
	from := common.HexToAddress("0x71562b71999873db5b286df957af199ec94617f7")
	to := common.HexToAddress("0x0d3ab14bbad3d99f4203bd7a11acb94882050e7e")

	orphanedBlock := orphanedChain[0].Blocks[0]
	blockNumberOrHash := rpc.BlockNumberOrHashWithHash(orphanedBlock.Hash(), true)
	var blockNumberOrHashRef *rpc.BlockNumberOrHash = &blockNumberOrHash

	if _, err := api.Call(context.Background(), ethapi.CallArgs{
		From: &from,
		To:   &to,
	}, blockNumberOrHashRef, nil); err != nil {
		if fmt.Sprintf("%v", err) != fmt.Sprintf("hash %s is not currently canonical", orphanedBlock.Hash().String()[2:]) {
			t.Errorf("wrong error: %v", err)
		}
	} else {
		t.Error("error expected")
	}
}

func TestUseBridgeReader(t *testing.T) {
	// test for Go's interface nil-ness caveat - https://codefibershq.com/blog/golang-why-nil-is-not-always-nil
	var br *mockBridgeReader
	api := NewBaseApi(nil, nil, (*freezeblocks.BlockReader)(nil), false, time.Duration(0), nil, datadir.Dirs{}, br)
	require.False(t, api.useBridgeReader)
	br = &mockBridgeReader{}
	api = NewBaseApi(nil, nil, (*freezeblocks.BlockReader)(nil), false, time.Duration(0), nil, datadir.Dirs{}, br)
	require.True(t, api.useBridgeReader)
}

var _ bridgeReader = mockBridgeReader{}

type mockBridgeReader struct{}

func (m mockBridgeReader) Events(context.Context, common.Hash, uint64) ([]*types.Message, error) {
	panic("mock")
}

func (m mockBridgeReader) EventTxnLookup(context.Context, common.Hash) (uint64, bool, error) {
	panic("mock")
}
