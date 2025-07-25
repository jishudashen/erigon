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

package args_test

import (
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/erigontech/erigon/cmd/devnet/args"
)

func TestNodeArgs(t *testing.T) {
	asMap := map[string]struct{}{}

	nodeArgs, _ := args.AsArgs(args.BlockProducer{
		NodeArgs: args.NodeArgs{
			DataDir:        filepath.Join("data", fmt.Sprintf("%d", 1)),
			PrivateApiAddr: "localhost:9092",
		},
		DevPeriod: 30,
	})

	for _, arg := range nodeArgs {
		asMap[arg] = struct{}{}
	}

	for _, arg := range producingNodeArgs("data", 1) {
		if _, ok := asMap[arg]; !ok {
			t.Fatal(arg, "missing")
		}

		delete(asMap, arg)
	}

	if len(asMap) > 0 {
		t.Fatal(asMap, "not found")
	}

	nodeArgs, _ = args.AsArgs(args.BlockConsumer{
		NodeArgs: args.NodeArgs{
			DataDir:        filepath.Join("data", fmt.Sprintf("%d", 2)),
			StaticPeers:    "enode",
			PrivateApiAddr: "localhost:9091",
		},
	})

	for _, arg := range nodeArgs {
		asMap[arg] = struct{}{}
	}

	for _, arg := range nonProducingNodeArgs("data", 2, "enode") {
		if _, ok := asMap[arg]; !ok {
			t.Fatal(arg, "missing")
		}

		delete(asMap, arg)
	}

	if len(asMap) > 0 {
		t.Fatal(asMap, "not found")
	}
}

func TestParameterFromArgument(t *testing.T) {
	enode := fmt.Sprintf("%q", "1234567")
	testCases := []struct {
		argInput    string
		paramInput  string
		expectedRes string
		expectedErr error
	}{
		{"--datadir", "./dev", "--datadir=./dev", nil},
		{"--chain", "dev", "--chain=dev", nil},
		{"--dev.period", "30", "--dev.period=30", nil},
		{"--staticpeers", enode, "--staticpeers=" + enode, nil},
		{"", "30", "", errInvalidArgument},
	}

	for _, testCase := range testCases {
		got, err := parameterFromArgument(testCase.argInput, testCase.paramInput)
		if got != testCase.expectedRes {
			t.Errorf("expected %s, got %s", testCase.expectedRes, got)
		}
		if err != testCase.expectedErr {
			t.Errorf("expected error: %s, got error: %s", testCase.expectedErr, err)
		}
	}
}

// errInvalidArgument for invalid arguments
var errInvalidArgument = errors.New("invalid argument")

// ParameterFromArgument merges the argument and parameter and returns a flag input string
func parameterFromArgument(arg, param string) (string, error) {
	if arg == "" {
		return "", errInvalidArgument
	}
	return fmt.Sprintf("%s=%s", arg, param), nil
}

const (
	// BuildDirArg is the build directory for the devnet executable
	buildDirArg = "./build/bin/devnet"
	// DataDirArg is the datadir flag
	dataDirArg = "--datadir"
	// ChainArg is the chain flag
	chainArg = "--chain"
	// DevPeriodArg is the dev.period flag
	devPeriodArg = "--dev.period"
	// ConsoleVerbosityArg is the log.console.verbosity flag
	consoleVerbosityArg = "--log.console.verbosity"
	// LogDirArg is the log.dir.path flag
	logDirArg = "--log.dir.path"
	// TorrentPortArg is the --torrent.port flag argument
	torrentPortArg = "--torrent.port"
	// Mine is the mine flag
	mine = "--mine"
	// NoDiscover is the nodiscover flag
	noDiscover = "--nodiscover"
	// PrivateApiAddrArg is the private.api.addr flag
	privateApiAddrArg = "--private.api.addr"
	// StaticPeersArg is the staticpeers flag
	staticPeersArg = "--staticpeers"
	// HttpApiArg is the http.api flag
	httpApiArg = "--http.api"
	// WSArg is the --ws flag for rpcdaemon
	wsArg = "--ws"

	// DataDirParam is the datadir parameter
	dataDirParam = "./dev"
	// ChainParam is the chain parameter
	chainParam = "dev"
	// DevPeriodParam is the dev.period parameter
	devPeriodParam = "30"
	// ConsoleVerbosityParam is the verbosity parameter for the console logs
	consoleVerbosityParam = "0"
	// LogDirParam is the log directory parameter for logging to disk
	logDirParam = "./cmd/devnet/debug_logs"
	// TorrentPortParam is the port parameter for the second node
	torrentPortParam = "42070"
	// PrivateApiParamMine is the private.api.addr parameter for the mining node
	privateApiParamMine = "localhost:9092"
	// PrivateApiParamNoMine is the private.api.addr parameter for the non-mining node
	privateApiParamNoMine = "localhost:9091"
	// HttpApiParam is the http.api default parameter for rpcdaemon
	httpApiParam = "admin,eth,erigon,web3,net,debug,trace,txpool,parity,ots"
)

// miningNodeArgs returns custom args for starting a mining node
func producingNodeArgs(dataDir string, nodeNumber int) []string {
	nodeDataDir := filepath.Join(dataDir, fmt.Sprintf("%d", nodeNumber))
	dataDirArg, _ := parameterFromArgument(dataDirArg, nodeDataDir)
	chainType, _ := parameterFromArgument(chainArg, chainParam)
	devPeriod, _ := parameterFromArgument(devPeriodArg, devPeriodParam)
	privateApiAddr, _ := parameterFromArgument(privateApiAddrArg, privateApiParamMine)
	httpApi, _ := parameterFromArgument(httpApiArg, httpApiParam)
	ws := wsArg
	consoleVerbosity, _ := parameterFromArgument(consoleVerbosityArg, consoleVerbosityParam)
	p2pProtocol, _ := parameterFromArgument("--p2p.protocol", "68")
	downloaderArg, _ := parameterFromArgument("--no-downloader", "true")
	httpPortArg, _ := parameterFromArgument("--http.port", "8545")
	wsPortArg, _ := parameterFromArgument("--ws.port", "8546")
	authrpcPortArg, _ := parameterFromArgument("--authrpc.port", "8551")
	natArg, _ := parameterFromArgument("--nat", "none")
	accountSlotsArg, _ := parameterFromArgument("--txpool.accountslots", "16")

	return []string{
		buildDirArg,
		dataDirArg,
		chainType,
		privateApiAddr,
		httpPortArg,
		wsPortArg,
		authrpcPortArg,
		mine,
		httpApi,
		ws,
		natArg,
		devPeriod,
		consoleVerbosity,
		p2pProtocol,
		downloaderArg,
		accountSlotsArg,
	}
}

// nonMiningNodeArgs returns custom args for starting a non-mining node
func nonProducingNodeArgs(dataDir string, nodeNumber int, enode string) []string {
	nodeDataDir := filepath.Join(dataDir, fmt.Sprintf("%d", nodeNumber))
	dataDirArg, _ := parameterFromArgument(dataDirArg, nodeDataDir)
	chainType, _ := parameterFromArgument(chainArg, chainParam)
	privateApiAddr, _ := parameterFromArgument(privateApiAddrArg, privateApiParamNoMine)
	staticPeers, _ := parameterFromArgument(staticPeersArg, enode)
	consoleVerbosity, _ := parameterFromArgument(consoleVerbosityArg, consoleVerbosityParam)
	torrentPort, _ := parameterFromArgument(torrentPortArg, torrentPortParam)
	p2pProtocol, _ := parameterFromArgument("--p2p.protocol", "68")
	downloaderArg, _ := parameterFromArgument("--no-downloader", "true")
	httpPortArg, _ := parameterFromArgument("--http.port", "8545")
	wsPortArg, _ := parameterFromArgument("--ws.port", "8546")
	httpApi, _ := parameterFromArgument(httpApiArg, "admin,eth,debug,net,trace,web3,erigon,txpool")
	authrpcPortArg, _ := parameterFromArgument("--authrpc.port", "8551")
	natArg, _ := parameterFromArgument("--nat", "none")
	ws := wsArg

	return []string{
		buildDirArg,
		dataDirArg,
		chainType,
		privateApiAddr,
		httpPortArg,
		wsPortArg,
		authrpcPortArg,
		httpApi,
		ws,
		natArg,
		staticPeers,
		noDiscover,
		consoleVerbosity,
		torrentPort,
		p2pProtocol,
		downloaderArg,
	}
}
